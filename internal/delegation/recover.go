// Purpose: fail interrupted delegations at startup without repeating a spawn.
// Pending effects become unknown-outcome before live resources are retired.

package delegation

import (
	"cmp"
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RecoveryDeps binds startup recovery to the daemon's existing stores and
// resource managers. Neither a driver nor a spawn operation is needed.
type RecoveryDeps struct {
	Effects *audit.EffectLog
	Store   *jobs.Store
	Fleet   interface {
		Abort(context.Context, capacity.Lease) error
		Release(context.Context, capacity.Lease) error
	}
	// ResolveLease reads the persisted reservation, including its commit state.
	// It must also find leases acquired before BindDelegationSpawn. An empty
	// lease ID means the record never acquired a lease; errors refuse cleanup.
	ResolveLease func(context.Context, jobs.DelegationRecord) (lease capacity.Lease, committed bool, err error)
	// ReleaseWorktree uses the daemon's worktree manager and is idempotent,
	// including when the record never acquired a worktree.
	ReleaseWorktree func(context.Context, jobs.DelegationRecord) error
	sweepLease      func(context.Context, string) error // nil uses the real marker sweep
}

// RecoverPendingSpawns marks all unterminated effect intents unknown-outcome,
// then sweeps each live delegation's marker, releases its worktree and lease,
// and fails its record with "daemon restart". It never replays an effect.
// A failed cleanup retains the live record for a subsequent recovery attempt;
// other live records are still processed. Call before serving delegate RPCs.
func RecoverPendingSpawns(ctx context.Context, d RecoveryDeps) error {
	if d.Effects == nil || d.Store == nil || d.Fleet == nil || d.ResolveLease == nil || d.ReleaseWorktree == nil {
		return invalidf("recovery: effects, store, fleet, lease resolver and worktree release are required")
	}
	if d.sweepLease == nil {
		d.sweepLease = agents.SweepLease
	}
	pending, err := d.Effects.PendingEffects(ctx)
	if err != nil {
		return err
	}
	for _, h := range pending {
		if _, err := d.Effects.MarkUnknownOutcome(ctx, h); err != nil {
			return err
		}
	}
	live, err := d.Store.LiveDelegations(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, rec := range live {
		first = cmp.Or(first, recoverDelegation(ctx, d, rec))
	}
	return first
}

func recoverDelegation(ctx context.Context, d RecoveryDeps, rec jobs.DelegationRecord) error {
	// No marker means the record never reached spawn: it is safe to clean up.
	if rec.LeaseMarker != "" {
		if err := d.sweepLease(ctx, rec.LeaseMarker); err != nil {
			return err
		}
	}
	if err := d.ReleaseWorktree(ctx, rec); err != nil {
		return err
	}
	lease, committed, err := d.ResolveLease(ctx, rec)
	if err != nil {
		return err
	}
	if rec.LeaseID != "" && lease.ID != rec.LeaseID {
		return integrityf("recovery: resolved lease differs from delegation %s", rec.ID)
	}
	if lease.ID != "" || lease.ExecutionID != "" { // a lease taken before the spawn binding has no ID yet
		if committed {
			err = d.Fleet.Release(ctx, lease)
		} else {
			err = d.Fleet.Abort(ctx, lease)
		}
		if err != nil {
			return err
		}
	}
	return d.Store.FinishDelegation(ctx, rec.ID, jobs.DelegationFailed, "daemon restart")
}

// ReservationReader reads the reservation bound to an execution; *capacity.Fleet satisfies it.
type ReservationReader interface {
	ReservationByExecution(ctx context.Context, executionID string) (economics.Reservation, bool, error)
}

// StoredLease is the RecoveryDeps.ResolveLease that rebuilds a record's lease from the
// execution and reservation ids stored with it, and reads its commit state from the
// persisted reservation, so a lease committed before a restart is released through the
// fleet after it. A record with no ids never leased (or predates them: refused, kept live).
func StoredLease(r ReservationReader) func(context.Context, jobs.DelegationRecord) (capacity.Lease, bool, error) {
	return func(ctx context.Context, rec jobs.DelegationRecord) (capacity.Lease, bool, error) {
		if rec.ExecutionID == "" {
			if rec.LeaseID == "" {
				return capacity.Lease{}, false, nil
			}
			return capacity.Lease{}, false, cascade.Newf(cascade.KindUnavailable,
				"recovery: lease %s of %s was stored without its execution id and cannot be rebuilt", rec.LeaseID, rec.ID)
		}
		rsv, ok, err := r.ReservationByExecution(ctx, rec.ExecutionID)
		switch {
		case err != nil:
			return capacity.Lease{}, false, err
		case !ok:
			return capacity.Lease{}, false, cascade.Newf(cascade.KindNotFound,
				"recovery: no reservation is bound to execution %s of %s", rec.ExecutionID, rec.ID)
		case rsv.ID != rec.ReservationID:
			return capacity.Lease{}, false, integrityf("recovery: execution %s of %s is bound to reservation %s, not %s",
				rec.ExecutionID, rec.ID, rsv.ID, rec.ReservationID)
		}
		lease := capacity.Lease{ID: rec.LeaseID, ExecutionID: rec.ExecutionID, ReservationID: rec.ReservationID,
			LaneID: conductor.LaneID(rec.Lane), RepoID: rsv.RepoID}
		return lease, rsv.State == economics.ReservationCommitted, nil
	}
}
