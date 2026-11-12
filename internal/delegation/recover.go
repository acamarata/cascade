// Purpose: fail interrupted delegations at startup without repeating a spawn.
// Pending effects become unknown-outcome before live resources are retired.

package delegation

import (
	"cmp"
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/agents"
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
	if lease.ID != "" {
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
