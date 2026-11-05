package jobs

// Purpose: DelegationByJobID's test: of several records for one job, the
// newest is returned; an unknown job is not found; an empty ID is refused.
//
// SPORT: jobs/delegation-record/ADD (P1-CI-06a).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDelegationByJobID(t *testing.T) {
	f := newDlgFix(t)
	ctx := context.Background()
	for i, id := range []string{"old", "new", "mid"} {
		r, _ := f.childOf(f.root, id)
		r.ParentSigned = f.rootSigned
		r.CreatedAt = dlgT0.Add([]time.Duration{time.Minute, 3 * time.Minute, 2 * time.Minute}[i])
		if err := f.st.InsertDelegation(ctx, r); err != nil {
			t.Fatalf("InsertDelegation(%s): %v", id, err)
		}
	}
	other, _ := f.childOf(f.root, "other")
	other.ParentSigned, other.JobID, other.CreatedAt = f.rootSigned, "job-2", dlgT0.Add(time.Hour)
	if err := f.st.InsertDelegation(ctx, other); err != nil {
		t.Fatalf("InsertDelegation(other): %v", err)
	}
	got, ok, err := f.st.DelegationByJobID(ctx, "job-1")
	if err != nil || !ok || got.ID != "new" || got.JobID != "job-1" {
		t.Fatalf("DelegationByJobID(job-1) = %q, %v, %v; want new", got.ID, ok, err)
	}
	if _, ok, err := f.st.DelegationByJobID(ctx, "job-none"); err != nil || ok {
		t.Fatalf("unknown job: ok=%v err=%v", ok, err)
	}
	_, _, err = f.st.DelegationByJobID(ctx, "")
	wantErr(t, err, cascade.KindInvalidInput, "job ID")
}
