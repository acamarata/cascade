// Purpose: the lease lifecycle after Lease: Commit (spawn confirmed), Abort
// (spawn failed), Release (terminal state), ExpireStale and the reservation
// read by execution id. Every transition runs through the one Reserver, so
// teardown is the single reverse chain (worktree removed before leases
// released).
//
// Inputs: a granted Lease (or an execution id).
// Outputs: nil, or the Reserver's typed error.
// Constraints: Abort and Release are idempotent. Abort of a held reservation
// rolls it back (rolled_back) by failing a Bind on it: the Reserver exposes no
// rollback entry point and a failed Bind is its defined rollback path, so the
// reverse teardown is not re-implemented here. A reservation already parked or
// committed cannot be bound, so Abort retires it through Release instead.
// ExpireStale delegates to Reserver.ExpireStale with FleetDeps.TTL as the
// liveness interval (one expiry rule).
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errAbortRequested is the sentinel cause Abort hands Bind to trigger the
// Reserver's rollback; it never reaches a caller.
var errAbortRequested = cascade.New(cascade.KindConflict, "capacity: lease aborted before spawn")

// Commit confirms a spawn: held to committed. It refuses a lease that does not
// own its reservation, and one whose reservation is no longer held or
// committed.
func (f *Fleet) Commit(ctx context.Context, l Lease) error {
	if _, err := f.ownedReservation(ctx, l); err != nil {
		return err
	}
	r, err := f.deps.Reserver.Commit(ctx, l.ReservationID)
	if err != nil {
		return err
	}
	if r.State != economics.ReservationCommitted {
		return cascade.Newf(cascade.KindConflict, "capacity: reservation %q is %s, not committed", r.ID, r.State)
	}
	return nil
}

// Abort rolls a lease back after a failed spawn: reverse teardown to
// rolled_back. A second Abort, or an Abort of a released lease, returns nil.
// The rollback runs on a context that ignores the caller's cancellation, so a
// cancelled caller cannot strand a held reservation.
func (f *Fleet) Abort(ctx context.Context, l Lease) error {
	ctx = context.WithoutCancel(ctx)
	r, err := f.ownedReservation(ctx, l)
	switch {
	case err != nil:
		return err
	case r.State.Terminal():
		f.index.drop(l.ID)
		return nil
	case r.State != economics.ReservationHeld:
		return f.Release(ctx, l)
	}
	_, err = f.deps.Reserver.Bind(ctx, r.ID, abortPlacement(l), func(context.Context, *sql.Tx, economics.Reservation) error {
		return errAbortRequested
	})
	if err != nil && !isCleanAbort(err) {
		return refuse(ErrReservationFailed, err, "abort lease %q", l.ID)
	}
	f.index.drop(l.ID)
	return nil
}

// ownedReservation reads the reservation of l's execution and refuses a lease
// that does not own it, so a mixed-up Lease value cannot retire another
// lease's reservation.
func (f *Fleet) ownedReservation(ctx context.Context, l Lease) (economics.Reservation, error) {
	r, found, err := f.deps.Reservations.GetByExecution(ctx, l.ExecutionID)
	switch {
	case err != nil:
		return economics.Reservation{}, err
	case !found:
		return economics.Reservation{}, cascade.Newf(cascade.KindNotFound, "capacity: reservation of execution %q not found", l.ExecutionID)
	case r.ID != l.ReservationID:
		return economics.Reservation{}, refuse(ErrLeaseUnknown, nil, "lease %q names reservation %q but its execution belongs to %q", l.ID, l.ReservationID, r.ID)
	}
	return r, nil
}

// abortPlacement is the placement Bind validates before it runs the failing
// BindFn; its values are rolled back with the transaction.
func abortPlacement(l Lease) economics.Placement {
	return economics.Placement{NodeID: l.NodeID, SelectedTier: string(l.SelectedTier), Sensitivity: l.Sensitivity.String(), DecisionID: l.DecisionID}
}

// Release retires a lease at its terminal state: reverse teardown to
// released. A second Release returns nil. Like Abort it ignores the caller's
// cancellation and refuses a lease that does not own its reservation.
func (f *Fleet) Release(ctx context.Context, l Lease) error {
	ctx = context.WithoutCancel(ctx)
	if _, err := f.ownedReservation(ctx, l); err != nil {
		return err
	}
	if _, err := f.deps.Reserver.Release(ctx, l.ReservationID); err != nil {
		return err
	}
	f.index.drop(l.ID)
	return nil
}

// ExpireStale retires the unheld stale reservations through the Reserver's
// one expiry rule and returns how many it retired. It then prunes the lease
// index of every lease whose reservation expiry, the start sweep or a direct
// Reserver call has retired, so the index cannot outgrow the live leases.
func (f *Fleet) ExpireStale(ctx context.Context) (int, error) {
	n, err := f.deps.Reserver.ExpireStale(ctx, f.deps.TTL)
	f.pruneIndex(ctx)
	return n, err
}

// ReservationByExecution returns the reservation bound to executionID.
func (f *Fleet) ReservationByExecution(ctx context.Context, executionID string) (economics.Reservation, bool, error) {
	return f.deps.Reservations.GetByExecution(ctx, executionID)
}

// isCleanAbort reports whether err is exactly the trigger Abort handed Bind:
// the Reserver returns the trigger unchanged after a clean teardown and joins
// it with the compensation errors after a failed one.
func isCleanAbort(err error) bool { return err == errAbortRequested } //nolint:errorlint // identity, not Kind
