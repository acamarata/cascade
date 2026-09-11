package jobs

// Purpose: Renew and the expiry sweep -- the 300-line-cap split from
//
//	lease.go (which keeps Acquire/Release/LeaseDefaults). Renew re-arms
//	a held lease's ttl window; SweepExpired moves any lease whose
//	ttl+expiry_grace has elapsed to expired_unconfirmed (never a direct
//	transfer to another holder -- lease_fence.go's Reclaim is the ONLY
//	path out of that state into a non-contending one).
//
// Inputs: a lease's natural key plus the caller's believed epoch
//
//	(Renew); nothing (SweepExpired walks every held/renewing row).
//
// Outputs: the renewed lease, or a typed error; the list of leases the
//
//	sweep moved to expired_unconfirmed.
//
// Constraints: all elapsed-time math reads m.clock.Now() -- never a bare
//
//	time.Now -- so lease_expiry_test.go drives every boundary
//	deterministically via runtime.FixedClock.Advance, including the
//	exact-instant expiry race (Renew and SweepExpired both evaluated at
//	the SAME injected instant).
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Renew re-arms holder's lease at (repoID, scopeGlob), accepted while
// now <= last issue/renew + ttl + expiry_grace (the same grace window
// the sweep uses, so a renew racing the sweep at the exact boundary
// instant is accepted, never expired out from under a live renewer).
// Success increments RenewCount and sets IssuedAt to now, re-arming the
// full ttl window; epoch is unchanged (a renew is not a new grant).
func (m *LeaseManager) Renew(ctx context.Context, repoID, scopeGlob string, epoch int64) (ResourceLease, error) {
	if !m.isController() {
		return ResourceLease{}, ErrNotController
	}
	now := m.clock.Now().Unix()
	var result ResourceLease
	txErr := m.store.withTx(ctx, func(tx *sql.Tx) error {
		lease, ok, err := getLeaseTx(ctx, tx, repoID, scopeGlob)
		if err != nil {
			return err
		}
		if !ok {
			return cascade.Newf(cascade.KindNotFound, "jobs: renew: no lease at %s/%s", repoID, scopeGlob)
		}
		if lease.Epoch != epoch {
			return cascade.Wrapf(cascade.KindConflict, ErrLeaseFenced,
				"jobs: renew %s/%s at epoch %d refused", repoID, scopeGlob, epoch)
		}
		deadline := lease.IssuedAt + lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
		if now > deadline {
			return cascade.Newf(cascade.KindConflict,
				"jobs: renew %s/%s refused: now %d is past the ttl+grace deadline %d", repoID, scopeGlob, now, deadline)
		}
		lease.IssuedAt = now
		lease.RenewCount++
		lease.State = LeaseHeld
		if err := putLeaseTx(ctx, tx, lease); err != nil {
			return err
		}
		result = lease
		return nil
	})
	if txErr != nil {
		return ResourceLease{}, txErr
	}
	if m.sink != nil {
		if err := m.sink.renewed(ctx, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// SweepExpired moves every held/renewing lease whose ttl+expiry_grace
// has elapsed (evaluated at m.clock.Now(), the SAME injected instant for
// every row this call examines) to expired_unconfirmed, raising the
// step-6 attention item for each. It never transfers a row to another
// holder -- that is lease_fence.go's Reclaim's job, and only after
// confirmed termination.
func (m *LeaseManager) SweepExpired(ctx context.Context) ([]ResourceLease, error) {
	if !m.isController() {
		return nil, ErrNotController
	}
	now := m.clock.Now().Unix()
	var expired []ResourceLease
	txErr := m.store.withTx(ctx, func(tx *sql.Tx) error {
		candidates, err := heldOrRenewingTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, l := range candidates {
			deadline := l.IssuedAt + l.TTLSeconds + m.defaults.ExpiryGraceSeconds
			if now <= deadline {
				continue
			}
			l.State = LeaseExpiredUnconfirmed
			if err := putLeaseTx(ctx, tx, l); err != nil {
				return err
			}
			expired = append(expired, l)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	if m.sink != nil {
		for _, l := range expired {
			if err := m.sink.expired(ctx, l); err != nil {
				return expired, err
			}
		}
	}
	return expired, nil
}
