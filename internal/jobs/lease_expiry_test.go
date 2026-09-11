package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestLeaseRenewWithinGraceSucceeds proves the accepted boundary: a
// renew presented AT the exact ttl+expiry_grace deadline (not one
// second before, not one after) succeeds, re-arms the ttl, and leaves
// the epoch unchanged.
func TestLeaseRenewWithinGraceSucceeds(t *testing.T) {
	m, clock, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline))

	renewed, err := m.Renew(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch)
	if err != nil {
		t.Fatalf("Renew at the exact deadline: %v", err)
	}
	if renewed.Epoch != granted.Lease.Epoch {
		t.Errorf("Renew changed epoch: got %d, want unchanged %d", renewed.Epoch, granted.Lease.Epoch)
	}
	if renewed.RenewCount != 1 {
		t.Errorf("RenewCount = %d, want 1", renewed.RenewCount)
	}
}

// TestLeaseRenewPastGraceRefused proves the other side of the same
// boundary: one second past the deadline, Renew refuses.
func TestLeaseRenewPastGraceRefused(t *testing.T) {
	m, clock, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))

	if _, err := m.Renew(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch); err == nil {
		t.Fatal("Renew one second past the deadline = nil error, want refusal")
	}
}

// TestLeaseSweepExpiredNeverSteals proves the "never silently stolen"
// invariant with the actual negative constructed: a scope past its
// deadline goes to expired_unconfirmed, NOT to a new holder, and an
// intersecting Acquire attempted immediately after the sweep is STILL
// refused (Contending() includes expired_unconfirmed).
func TestLeaseSweepExpiredNeverSteals(t *testing.T) {
	m, clock, _ := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))

	expired, err := m.SweepExpired(ctx)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if len(expired) != 1 || expired[0].State != LeaseExpiredUnconfirmed {
		t.Fatalf("SweepExpired = %+v, want exactly one expired_unconfirmed row", expired)
	}
	if expired[0].Holder != "job-a" {
		t.Errorf("expired lease still names holder %q, want unchanged job-a (never transferred)", expired[0].Holder)
	}

	attempt, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-b")
	if err != nil {
		t.Fatalf("post-sweep Acquire: %v", err)
	}
	if attempt.Granted {
		t.Fatal("post-sweep Acquire on an expired_unconfirmed scope was granted -- the lease was silently stolen")
	}
}

// TestLeaseExpiryRaceAtExactInstant is the required expiry-race proof:
// a lease whose deadline is exactly now, with a Renew and a
// SweepExpired call launched concurrently (real goroutines, -race) at
// that SAME injected instant. Both operations run inside Store.withTx's
// single-connection transaction, so exactly one of them commits first;
// this test asserts the row ends up in EXACTLY one of the two
// self-consistent outcomes -- (a) Renew won: state held, ttl re-armed,
// epoch unchanged -- or (b) the sweep won: state expired_unconfirmed,
// held/renewed that never actually happened has no fingerprint in the
// stored row -- and that no other outcome (e.g. a corrupted mixed
// write, or both succeeding) is possible.
func TestLeaseExpiryRaceAtExactInstant(t *testing.T) {
	m, clock, store := newTestLeaseManager(t)
	ctx := context.Background()
	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline)) // now == deadline exactly, for BOTH goroutines below

	var wg sync.WaitGroup
	renewResult := make(chan error, 1)
	sweepResult := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := m.Renew(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch)
		renewResult <- err
	}()
	go func() {
		defer wg.Done()
		_, err := m.SweepExpired(ctx)
		sweepResult <- err
	}()
	wg.Wait()

	renewErr := <-renewResult
	sweepErr := <-sweepResult
	if sweepErr != nil {
		t.Fatalf("SweepExpired: %v", sweepErr)
	}

	final, ok, err := store.GetLease(ctx, "repo-1", granted.Lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("GetLease after race: ok=%v err=%v", ok, err)
	}
	switch final.State {
	case LeaseHeld:
		if renewErr != nil {
			t.Errorf("row ended held but Renew reported an error: %v", renewErr)
		}
		if final.Epoch != granted.Lease.Epoch {
			t.Errorf("held-outcome epoch = %d, want unchanged %d", final.Epoch, granted.Lease.Epoch)
		}
	case LeaseExpiredUnconfirmed:
		// The sweep landed first; now == deadline is still "at the
		// deadline", exactly the accepted boundary Renew's own
		// contract names -- so a Renew that ran AFTER the sweep had
		// already flipped the row is racing against a state that no
		// longer satisfies "now <= last issue/renew + ttl + grace" for
		// a HELD row, and correctly refuses.
		if renewErr == nil {
			t.Errorf("row ended expired_unconfirmed but Renew reported success -- a renew must never succeed against an already-swept row")
		}
	case LeaseRenewing, LeaseExpiredOrphaned, LeaseReleased:
		t.Fatalf("row ended in state %v, want held or expired_unconfirmed (no third outcome is possible)", final.State)
	}
}

// secondsUntil returns the time.Duration clock must Advance by so its
// next Now() reports exactly targetUnix seconds since the epoch.
func secondsUntil(clock *runtime.FixedClock, targetUnix int64) time.Duration {
	return time.Duration(targetUnix-clock.Now().Unix()) * time.Second
}
