package jobs

// Purpose: Readmit's adopt/reserve paths, the failure->pending rule and
//
//	the nil-seam refusals (P1-TOP-04c).
//
// SPORT: jobs/scheduler/ADD (tests) (P1-TOP-04c).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// seamRecorder counts adopt and reserve calls and returns err from both.
type seamRecorder struct {
	adopted, reserved []string
	err               error
}

func (r *seamRecorder) adopt(_ context.Context, id string) error {
	r.adopted = append(r.adopted, id)
	return r.err
}

func (r *seamRecorder) reserve(_ context.Context, jobID string) error {
	r.reserved = append(r.reserved, jobID)
	return r.err
}

func wantTransition(t *testing.T, got StateTransition, jobID string, to JobState) {
	t.Helper()
	want := StateTransition{JobID: jobID, From: JobStateLeased, To: to}
	if got != want {
		t.Fatalf("transition = %#v, want %#v", got, want)
	}
}

func TestReadmitReenteredJobRuns(t *testing.T) {
	sched := NewScheduler(nil)

	// A held reservation is adopted, never re-reserved.
	held := &seamRecorder{}
	tr, err := sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j1", State: JobStateLeased, ReservationID: "res-1"}, held.adopt, held.reserve)
	if err != nil {
		t.Fatalf("Readmit(adopt): %v", err)
	}
	wantTransition(t, tr, "j1", JobStateRunning)
	if len(held.adopted) != 1 || held.adopted[0] != "res-1" || len(held.reserved) != 0 {
		t.Fatalf("adopted=%v reserved=%v, want exactly one adopt of res-1", held.adopted, held.reserved)
	}

	// No surviving reservation: a fresh one is reserved.
	fresh := &seamRecorder{}
	tr, err = sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j2", State: JobStateLeased}, fresh.adopt, fresh.reserve)
	if err != nil {
		t.Fatalf("Readmit(reserve): %v", err)
	}
	wantTransition(t, tr, "j2", JobStateRunning)
	if len(fresh.reserved) != 1 || fresh.reserved[0] != "j2" || len(fresh.adopted) != 0 {
		t.Fatalf("adopted=%v reserved=%v, want exactly one reserve for j2", fresh.adopted, fresh.reserved)
	}
}

func TestReadmitReserveFailureReturnsPending(t *testing.T) {
	sched := NewScheduler(nil)
	seamErr := cascade.New(cascade.KindQuotaExhausted, "quota unavailable")
	cases := map[string]ReadmitRequest{
		"reserve": {JobID: "j1", State: JobStateLeased},
		"adopt":   {JobID: "j1", State: JobStateLeased, ReservationID: "res-1"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &seamRecorder{err: seamErr}
			tr, err := sched.Readmit(controllerCtx(), req, rec.adopt, rec.reserve)
			if err != seamErr || err.Error() != seamErr.Error() {
				t.Fatalf("err = %v, want the seam's own error", err)
			}
			wantTransition(t, tr, "j1", JobStatePending)
		})
	}

	// Paired positive: the same request with a healthy seam runs.
	ok := &seamRecorder{}
	tr, err := sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j1", State: JobStateLeased}, ok.adopt, ok.reserve)
	if err != nil {
		t.Fatalf("Readmit(healthy): %v", err)
	}
	wantTransition(t, tr, "j1", JobStateRunning)
}

func TestReadmitNilReserverRefuses(t *testing.T) {
	sched := NewScheduler(nil)
	rec := &seamRecorder{}
	tr, err := sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j1", State: JobStateLeased, ReservationID: "res-1"}, rec.adopt, nil)
	if err != ErrNilReserver || err.Error() != ErrNilReserver.Error() {
		t.Fatalf("err = %v, want ErrNilReserver", err)
	}
	if tr != (StateTransition{}) || len(rec.adopted) != 0 {
		t.Fatalf("transition=%#v adopted=%v, want no transition and no seam call", tr, rec.adopted)
	}

	// A held reservation with no adopt seam also refuses.
	tr, err = sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j1", State: JobStateLeased, ReservationID: "res-1"}, nil, rec.reserve)
	if err != ErrNilAdopter || tr != (StateTransition{}) || len(rec.reserved) != 0 {
		t.Fatalf("err=%v transition=%#v reserved=%v, want ErrNilAdopter and nothing reserved", err, tr, rec.reserved)
	}

	// A job not at leased, and a non-controller ctx, refuse.
	if _, err := sched.Readmit(controllerCtx(), ReadmitRequest{JobID: "j1", State: JobStateRunning}, rec.adopt, rec.reserve); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput for a running job", err)
	}
	if _, err := sched.Readmit(nodeCtx(), ReadmitRequest{JobID: "j1", State: JobStateLeased}, rec.adopt, rec.reserve); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("err = %v, want KindPermissionDenied on a node ctx", err)
	}
	if len(rec.adopted)+len(rec.reserved) != 0 {
		t.Fatalf("seams called on a refused request: adopted=%v reserved=%v", rec.adopted, rec.reserved)
	}
}
