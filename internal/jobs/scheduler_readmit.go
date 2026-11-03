package jobs

// Purpose: re-admit a job Resume re-entered at `leased` (R-16.68c)
//
//	through the fleet reservation ledger instead of a per-node governor
//	permit (P1-TOP-04).
//
// Inputs: a controller ctx, a ReadmitRequest, and the AdoptFn/ReserveFn
//
//	seams the composition root binds to the Reserver (contract
//	fleet-reservation).
//
// Outputs: the StateTransition the coordinator applies: leased->running
//
//	when the reservation was adopted or taken, leased->pending (with the
//	seam's error) when it was not.
//
// Constraints: fail closed. A nil ReserveFn refuses; a held reservation id
//
//	with a nil AdoptFn refuses; a job not at `leased` refuses. Nothing
//	here imports internal/fleet/economics and no permit is held.
//
// SPORT: jobs/scheduler/ADD (P1-TOP-04c).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNilReserver is returned when Readmit is called without a ReserveFn.
var ErrNilReserver = cascade.New(cascade.KindInvalidInput, "jobs: readmit needs a reserve seam")

// ErrNilAdopter is returned when Readmit must adopt a held reservation
// but was given no AdoptFn.
var ErrNilAdopter = cascade.New(cascade.KindInvalidInput, "jobs: readmit needs an adopt seam for a held reservation")

// ReadmitRequest names the re-entered job. ReservationID is the
// reservation the job held before the restart; empty means none survives
// and a fresh one is reserved.
type ReadmitRequest struct {
	JobID         string
	State         JobState
	ReservationID string
}

// Readmit re-admits a job re-entered at `leased`: it adopts the held
// reservation (or reserves a fresh one when none is held) and returns
// leased->running. Any seam failure returns leased->pending together with
// that error, so the job waits for a normal admission.
func (s *Scheduler) Readmit(ctx context.Context, req ReadmitRequest, adopt AdoptFn, reserve ReserveFn) (StateTransition, error) {
	if err := requireControllerCtx(ctx, "job.readmit"); err != nil {
		return StateTransition{}, err
	}
	if reserve == nil {
		return StateTransition{}, ErrNilReserver
	}
	if req.JobID == "" {
		return StateTransition{}, cascade.New(cascade.KindInvalidInput, "jobs: readmit: empty job id")
	}
	if req.State != JobStateLeased {
		return StateTransition{}, cascade.Newf(cascade.KindInvalidInput,
			"jobs: readmit: job %q is %q, want %q", req.JobID, req.State, JobStateLeased)
	}
	if req.ReservationID != "" && adopt == nil {
		return StateTransition{}, ErrNilAdopter
	}
	var err error
	if req.ReservationID != "" {
		err = adopt(ctx, req.ReservationID)
	} else {
		err = reserve(ctx, req.JobID)
	}
	if err != nil {
		return StateTransition{JobID: req.JobID, From: JobStateLeased, To: JobStatePending}, err
	}
	return StateTransition{JobID: req.JobID, From: JobStateLeased, To: JobStateRunning}, nil
}
