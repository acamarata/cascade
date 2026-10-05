// Purpose: the reverse teardown chain every rollback, release, expiry
//
//	and lost-lease path runs, plus Commit and Release, both idempotent
//	over an already-committed or terminal row.
//
// Inputs: a Reservation carrying the resources actually acquired so far.
// Outputs: teardown, failAndRollback, retire, Commit, Release.
// Constraints: teardown attempts EVERY compensation even when an
//
//	earlier one errors, in the exact reverse of the application order
//	(worktree removed before leases released), joining every
//	compensation error under ErrReservationRollback; it never deletes the
//	row. Compensation runs on a context that ignores the caller's
//	cancellation, so a cancelled caller cannot strand a lease. ROLLBACK
//	POLICY: a clean teardown returns the ORIGINAL triggering error
//	unchanged; a failing teardown returns errors.Join(trigger, teardown
//	error), so neither is swallowed. The terminal state is written only
//	through the store's transition (rolled_back where the table allows
//	it, released from committed, the one legal terminal move there).
//	No compensation is retried: a failed one keeps its handle (the
//	worktree id or lease ids) on the terminal row for an operator, and
//	Sweep and ExpireStale never revisit a terminal row.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// teardown runs worktree removal then lease release, attempting both
// even when the first errors. It never mutates r's State.
func (rv *Reserver) teardown(ctx context.Context, r Reservation) (Reservation, error) {
	var errs []error
	if r.WorktreeID != "" {
		if err := rv.seams.RemoveWorktree(ctx, r.WorktreeID); err != nil {
			errs = append(errs, err)
		} else {
			r = setStep(r, StepWorktree, StepCompensated, r.WorktreeID)
			r.WorktreeID = ""
		}
	}
	if len(r.LeaseIDs) > 0 {
		if err := rv.seams.ReleaseLeases(ctx, r.LeaseIDs); err != nil {
			errs = append(errs, err)
		} else {
			r = setStep(r, StepLeases, StepCompensated, findStepHandle(r, StepLeases))
			r.LeaseIDs = nil
		}
	}
	if len(errs) == 0 {
		return r, nil
	}
	joined := append([]error{ErrReservationRollback}, errs...)
	return r, cascade.Wrap(cascade.KindInternal, errors.Join(joined...), "economics: rollback reported one or more compensation errors")
}

// findStepHandle returns the handle of the most recent Step for name.
func findStepHandle(r Reservation, name StepName) string {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Step == name {
			return r.Steps[i].Handle
		}
	}
	return ""
}

// terminalFor is the terminal state a rollback moves from: rolled_back
// from held or parked, released from committed.
func terminalFor(s ReservationState) ReservationState {
	if TransitionAllowed(s, ReservationRolledBack) {
		return ReservationRolledBack
	}
	return ReservationReleased
}

// retire tears r down and moves it to to, removing it from the in-memory
// set. The returned error joins every compensation and persistence
// failure; the row is returned as persisted.
func (rv *Reserver) retire(ctx context.Context, r Reservation, to ReservationState) (Reservation, error) {
	ctx = context.WithoutCancel(ctx)
	defer rv.untrack(r.ID)
	r, rbErr := rv.teardown(ctx, r)
	stored, err := rv.store.transitionRow(ctx, r, to)
	if err != nil {
		return Reservation{}, errors.Join(rbErr, err)
	}
	return stored, rbErr
}

// failAndRollback retires r to its rollback state and returns triggerErr
// unchanged on a clean teardown, or joined with the teardown error.
func (rv *Reserver) failAndRollback(ctx context.Context, r Reservation, triggerErr error) (Reservation, error) {
	stored, err := rv.retire(ctx, r, terminalFor(r.State))
	if err != nil {
		return stored, errors.Join(triggerErr, err)
	}
	return stored, triggerErr
}

// Commit moves a held reservation to committed, performing no
// acquisition. Idempotent: an already committed or terminal row is
// returned unchanged with no second transition.
func (rv *Reserver) Commit(ctx context.Context, id string) (Reservation, error) {
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.State == ReservationCommitted || r.State.Terminal() {
		return r, nil
	}
	return rv.store.transitionState(ctx, r, ReservationCommitted)
}

// Release runs on the job's terminal state: the same reverse teardown as
// rollback, recording released. Idempotent: a terminal row is returned
// unchanged with no second teardown.
func (rv *Reserver) Release(ctx context.Context, id string) (Reservation, error) {
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.State.Terminal() {
		return r, nil
	}
	return rv.retire(ctx, r, ReservationReleased)
}

// mustGet reads id, reporting a missing row as KindNotFound.
func (rv *Reserver) mustGet(ctx context.Context, id string) (Reservation, error) {
	r, ok, err := rv.store.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if !ok {
		return Reservation{}, cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	return r, nil
}
