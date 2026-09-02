// Purpose: the domain-level advisory lock (task 2) that makes "two daemons
//   must never run the same scheduled job concurrently" true. Scheduler
//   acquires exactly one of these before Activate succeeds; a second
//   daemon process opening the same Store namespace fails to acquire it
//   and gets a typed cascade.KindConflict error, per §D-3
//   write-arbitration.
// Inputs: a provider.Store namespace shared by every daemon that might
//   contend for the lock, a per-instance ownerID, a lease TTL, and an
//   injected Clock.
// Outputs: acquire/renew/release results, or a cascade.KindConflict error
//   on contention.
// Constraints: no bare time.Now (R-14.11). The lock is LEASE-based, not
//   held indefinitely: Acquire/Renew stamp an expiry (now + ttl); a lease
//   that is not renewed before it expires becomes stealable by any other
//   owner. This is deliberate — a lock a crashed process holds forever
//   would mean the job it guarded never runs again, which is exactly the
//   failure mode a "the first holder's lock is unaffected" contract must
//   not also produce for every OTHER daemon after a crash. See
//   scheduler.go's package doc for the three ways a lease actually goes
//   away (graceful Close, a fatal in-Tick error, or simply not being
//   renewed before it expires).
// SPORT: internal.events.scheduler.advisoryLock/ADDED (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// lockKey is the single Store key the domain-level advisory lock lives
// under. There is exactly one lock per (store, namespace) pair — the
// contract's "domain-level exclusive advisory lock", not a per-job lock.
const lockKey = "sched:lock"

// lockRecord is the lock's JSON wire shape.
type lockRecord struct {
	Owner     string `json:"owner"`
	ExpiresAt string `json:"expires_at"`
}

// parsedLock is lockRecord after timestamp decoding.
type parsedLock struct {
	Owner     string
	ExpiresAt time.Time
}

// advisoryLock is one Scheduler instance's handle on the shared lock
// record. The zero value is not usable; construct with newAdvisoryLock.
type advisoryLock struct {
	store     provider.Store
	namespace string
	ownerID   string
	ttl       time.Duration
	clock     runtime.Clock
}

func newAdvisoryLock(store provider.Store, namespace, ownerID string, ttl time.Duration, clock runtime.Clock) *advisoryLock {
	return &advisoryLock{store: store, namespace: namespace, ownerID: ownerID, ttl: ttl, clock: clock}
}

// Acquire takes the lock. It succeeds if the lock is absent, already
// expired, or already held by this same ownerID (an idempotent
// re-acquire); it fails with a cascade.KindConflict error if another
// owner's lease is still live — the first holder's lock is left
// completely unaffected by a failed Acquire from a second owner (the
// whole read-then-CompareAndSwap runs inside one Store.Tx, so a losing
// Acquire never partially writes).
func (l *advisoryLock) Acquire(ctx context.Context) error {
	now := l.clock.Now()
	return l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		old, existing, err := readLock(ctx, tx, l.namespace)
		if err != nil {
			return err
		}
		if existing != nil && existing.ExpiresAt.After(now) && existing.Owner != l.ownerID {
			return cascade.Newf(cascade.KindConflict,
				"scheduler: advisory lock held by %q until %s", existing.Owner, existing.ExpiresAt.Format(time.RFC3339Nano))
		}
		return writeLock(ctx, tx, l.namespace, old, l.ownerID, now.Add(l.ttl))
	})
}

// Renew extends this owner's lease by ttl from now. It fails with a
// cascade.KindConflict error if this owner no longer holds a live
// lease — either it expired and another owner stole it, or it was never
// acquired — so a caller that fails to Renew knows unambiguously that it
// no longer has exclusive access and must stop firing jobs.
func (l *advisoryLock) Renew(ctx context.Context) error {
	now := l.clock.Now()
	return l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		old, existing, err := readLock(ctx, tx, l.namespace)
		if err != nil {
			return err
		}
		if existing == nil || existing.Owner != l.ownerID || !existing.ExpiresAt.After(now) {
			return cascade.New(cascade.KindConflict, "scheduler: advisory lock lease lost, cannot renew")
		}
		return writeLock(ctx, tx, l.namespace, old, l.ownerID, now.Add(l.ttl))
	})
}

// Release relinquishes the lock if, and only if, this owner currently
// holds it (live or already expired) — releasing a lock this owner does
// not hold (already stolen by someone else, or never acquired) is a
// deliberate no-op, not an error, so a defensive "release on the way out"
// call is always safe regardless of what already happened to the lease.
func (l *advisoryLock) Release(ctx context.Context) error {
	return l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		_, existing, err := readLock(ctx, tx, l.namespace)
		if err != nil {
			return err
		}
		if existing == nil || existing.Owner != l.ownerID {
			return nil
		}
		if err := tx.Delete(ctx, l.namespace, lockKey); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "scheduler: release advisory lock")
		}
		return nil
	})
}

// readLock returns the lock record's raw bytes (for CompareAndSwap's `old`
// argument) and its decoded form, or (nil, nil, nil) if no lock record
// exists yet.
func readLock(ctx context.Context, tx provider.Tx, namespace string) ([]byte, *parsedLock, error) {
	data, err := tx.Get(ctx, namespace, lockKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil, nil
		}
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "scheduler: read advisory lock")
	}
	var rec lockRecord
	if jerr := json.Unmarshal(data, &rec); jerr != nil {
		return nil, nil, cascade.Wrap(cascade.KindIntegrity, jerr, "scheduler: corrupt advisory lock record")
	}
	expires, perr := time.Parse(time.RFC3339Nano, rec.ExpiresAt)
	if perr != nil {
		return nil, nil, cascade.Wrap(cascade.KindIntegrity, perr, "scheduler: corrupt advisory lock expiry")
	}
	return data, &parsedLock{Owner: rec.Owner, ExpiresAt: expires}, nil
}

// writeLock CompareAndSwaps the lock record from old to a fresh record for
// owner expiring at expiresAt. A CAS race lost to a concurrent writer
// (another Acquire/Renew that landed between this call's read and write)
// surfaces as the driver's own cascade.KindConflict error, unwrapped —
// exactly the same Kind a losing Acquire already returns, so callers never
// see two different error shapes for what is, from their perspective, one
// outcome: someone else has the lock.
func writeLock(ctx context.Context, tx provider.Tx, namespace string, old []byte, owner string, expiresAt time.Time) error {
	rec := lockRecord{Owner: owner, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)}
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "scheduler: encode advisory lock record")
	}
	return tx.CompareAndSwap(ctx, namespace, lockKey, old, data)
}
