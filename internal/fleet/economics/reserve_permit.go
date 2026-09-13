// Purpose: the R-21.122 per-adapter-call permit lifetime: WithPermit
//
//	takes the M/S-26.T2 permit immediately before one adapter call and
//	releases it when that call returns, never inside Reserve itself, and
//	Park/Unpark move a reservation to and from the parked state so a
//	blocked, queued, retrying or batch-waiting reservation holds none.
//
// Inputs: a live *Reserver, a reservation id already in state held or
//
//	committed, and the caller's adapter-call closure.
// Outputs: WithPermit, Park, Unpark.
// Constraints: this is the ONLY permit path (R-16.64 -- no second
//
//	admission entry point); a parked reservation is refused (R-21.122);
//	the permit is released via defer even when fn panics or errors, so
//	no adapter call can leak an admission slot.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// WithPermit is the ONE R-16.64 permit path this package exposes: it
// takes a governor.Permit at governor.PriorityDefault immediately before
// fn runs and releases it (exactly once, via defer) when fn returns,
// regardless of whether fn errors. Reserve itself never calls this --
// the permit is per-adapter-call, not per-reservation-lifetime
// (R-21.122).
func (rv *Reserver) WithPermit(ctx context.Context, reservationID string, fn func(context.Context) error) error {
	if reservationID == "" {
		return cascade.New(cascade.KindInvalidInput, "economics: WithPermit requires a non-empty reservationID")
	}
	if fn == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: WithPermit requires a non-nil fn")
	}
	r, ok, err := rv.store.Get(ctx, reservationID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "economics: reservation %q not found", reservationID)
	}
	if r.State != ReservationHeld && r.State != ReservationCommitted {
		return cascade.Wrapf(cascade.KindConflict, ErrReservationTransition, "reservation %q is %s: parked and terminal reservations hold no permit", reservationID, r.State)
	}
	permit, err := rv.permit(ctx, governor.AdmissionRequest{Kind: "model-call", Priority: governor.PriorityDefault})
	if err != nil {
		return err
	}
	defer permit.Release()
	return fn(ctx)
}

// Park moves reservationID from held to parked (R-21.122): a blocked,
// queued, retrying or async-batch-waiting reservation, which holds no
// permit while parked. Illegal from any other state.
func (rv *Reserver) Park(ctx context.Context, reservationID string) (Reservation, error) {
	return rv.store.Transition(ctx, reservationID, ReservationParked)
}

// Unpark moves reservationID from parked back to held. Illegal from any
// other state.
func (rv *Reserver) Unpark(ctx context.Context, reservationID string) (Reservation, error) {
	return rv.store.Transition(ctx, reservationID, ReservationHeld)
}
