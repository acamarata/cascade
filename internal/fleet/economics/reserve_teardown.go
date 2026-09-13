// Purpose: the reverse teardown chain reserve.go's Reserve rolls back
//
//	through on failure, plus Commit and Release, both idempotent over an
//	already-terminal row (300-line cap split from reserve.go).
//
// Inputs: a Reservation carrying the resources actually acquired so far.
// Outputs: teardown, failAndRollback, Commit, Release.
// Constraints: teardown attempts EVERY compensation even when an
//
//	earlier one errors, in the exact reverse of the R-21.35 application
//	order (worktree removed before leases released -- LIFO), joining
//	every compensation error under ErrReservationRollback; it never
//	deletes the row. ROLLBACK POLICY (decided, tested): when teardown
//	itself reports no error, Reserve/Release return the ORIGINAL
//	triggering error unchanged; when teardown itself also errors, the
//	original error and ErrReservationRollback's joined compensation
//	errors are BOTH returned via errors.Join, so neither is swallowed.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// markStepCompensated marks the most recently appended Step for name
// compensated.
func markStepCompensated(r Reservation, name StepName) Reservation {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Step == name {
			r.Steps[i].State = StepCompensated
			break
		}
	}
	return r
}

// teardown runs the exact reverse of the R-21.35 application order --
// worktree removed, then leases released -- attempting every step even
// when an earlier one errors. It never deletes r and never mutates r's
// State; the caller sets the final State (rolled_back or released).
func (rv *Reserver) teardown(ctx context.Context, r Reservation) (Reservation, error) {
	var errs []error
	if r.WorktreeID != "" {
		if err := rv.removeWorktree(ctx, r.WorktreeID); err != nil {
			errs = append(errs, err)
		} else {
			r = markStepCompensated(r, StepWorktree)
			r.WorktreeID = ""
		}
	}
	if len(r.LeaseIDs) > 0 {
		if err := rv.releaseLeases(ctx, r.LeaseIDs); err != nil {
			errs = append(errs, err)
		} else {
			r = markStepCompensated(r, StepLeases)
			r.LeaseIDs = nil
		}
	}
	if len(errs) == 0 {
		return r, nil
	}
	joined := append([]error{ErrReservationRollback}, errs...)
	return r, cascade.Wrap(cascade.KindInternal, errors.Join(joined...), "economics: rollback reported one or more compensation errors")
}

// failAndRollback tears r down and persists it as rolled_back, then
// returns triggerErr unchanged if teardown was clean, or
// errors.Join(triggerErr, teardown error) if teardown itself failed --
// see this file's ROLLBACK POLICY doc comment.
func (rv *Reserver) failAndRollback(ctx context.Context, r Reservation, triggerErr error) (Reservation, error) {
	r, rbErr := rv.teardown(ctx, r)
	r.State = ReservationRolledBack
	if err := rv.store.Replace(ctx, r); err != nil {
		if rbErr != nil {
			return Reservation{}, errors.Join(triggerErr, rbErr, err)
		}
		return Reservation{}, errors.Join(triggerErr, err)
	}
	if rbErr != nil {
		return r, errors.Join(triggerErr, rbErr)
	}
	return r, triggerErr
}

// Commit moves a held reservation to committed, performing no
// acquisition of its own. Idempotent: a reservation already committed
// or already terminal (released/rolled_back) is returned unchanged with
// no error and no second transition.
func (rv *Reserver) Commit(ctx context.Context, id string) (Reservation, error) {
	r, ok, err := rv.store.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if !ok {
		return Reservation{}, cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	if r.State == ReservationCommitted || r.State.Terminal() {
		return r, nil
	}
	return rv.store.Transition(ctx, id, ReservationCommitted)
}

// Release runs on the job's terminal state: the same reverse teardown
// as rollback, recording state released instead of rolled_back.
// Idempotent: an already-terminal reservation is returned unchanged
// with no second teardown.
func (rv *Reserver) Release(ctx context.Context, id string) (Reservation, error) {
	r, ok, err := rv.store.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if !ok {
		return Reservation{}, cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", id)
	}
	if r.State.Terminal() {
		return r, nil
	}
	r, rbErr := rv.teardown(ctx, r)
	r.State = ReservationReleased
	if err := rv.store.Replace(ctx, r); err != nil {
		return Reservation{}, err
	}
	if rbErr != nil {
		return r, rbErr
	}
	return r, nil
}
