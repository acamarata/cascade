package jobs

// Purpose: R-16.82 regression coverage for the resume-only restricted
//
//	transition path (store_job_resume.go): the store-level guard, the
//	public-path refusal proof, and the resume-edge-set proof, split out
//	of scheduler_resume_test.go's own Resume-driven tests to keep both
//	files under the 300-line cap.
//
// SPORT: jobs/scheduler/FIX (R-16.82, DEFECT-resume-transition-not-applied).

import (
	"context"
	"testing"
)

// TestPutResumeTransition_RefusesExpiredUnconfirmedAtTheTransition drives
// the restricted apply path directly (not through Resume's own upstream
// staleness scan), proving the precondition is re-checked AT the
// transition itself -- R-16.82's "not merely upstream" requirement.
func TestPutResumeTransition_RefusesExpiredUnconfirmedAtTheTransition(t *testing.T) {
	store := newResumeTestStore(t)
	seedRunningJobWithLease(t, store, "j1", 0, LeaseExpiredUnconfirmed)

	err := store.PutResumeTransition(controllerCtx(), "j1", 10_000)
	if err == nil {
		t.Fatal("PutResumeTransition on an expired_unconfirmed holder: err = nil, want refusal")
	}
	got, ok, gerr := store.GetJob(context.Background(), "j1")
	if gerr != nil || !ok {
		t.Fatalf("GetJob(j1): ok=%v err=%v", ok, gerr)
	}
	if got.State != JobStateRunning {
		t.Fatalf("job state in store = %q, want unchanged %q after refusal", got.State, JobStateRunning)
	}
}

// TestPutResumeTransition_RefusesOnNonController proves the store-level
// apply path is itself gated by the R-21.169 controller guard, not only
// by Resume's own upstream check -- reusing requireControllerCtx rather
// than trusting the caller.
func TestPutResumeTransition_RefusesOnNonController(t *testing.T) {
	store := newResumeTestStore(t)
	seedRunningJobWithLease(t, store, "j1", 0, LeaseHeld)

	if err := store.PutResumeTransition(nodeCtx(), "j1", 10_000); err == nil {
		t.Fatal("PutResumeTransition on a node daemon: err = nil, want refusal")
	}
}

// TestTransitionAllowed_RefusesRunningToLeasedOnPublicPath proves
// publicEdges was correctly left untouched by R-16.82: the public store
// path (TransitionAllowed, and therefore Store.PutTransition) must
// continue to REFUSE running->leased with its typed transition error, so
// no caller can demote a job still executing on a live worker.
func TestTransitionAllowed_RefusesRunningToLeasedOnPublicPath(t *testing.T) {
	if err := TransitionAllowed(JobStateRunning, JobStateLeased); err == nil {
		t.Fatal("TransitionAllowed(running, leased): err = nil, want the public path to refuse this edge")
	}

	store := newResumeTestStore(t)
	seedRunningJobWithLease(t, store, "j1", 0, LeaseHeld)
	if err := store.PutTransition(context.Background(), "j1", JobStateLeased, 10_000); err == nil {
		t.Fatal("PutTransition(running->leased) on the public path: err = nil, want refusal")
	}
}

// TestResumeTransitionAllowed_OnlyPermitsRunningToLeased proves the
// resume-only restricted edge set is exactly one edge -- it must not
// silently widen into a second public/policy path.
func TestResumeTransitionAllowed_OnlyPermitsRunningToLeased(t *testing.T) {
	if err := ResumeTransitionAllowed(JobStateRunning, JobStateLeased); err != nil {
		t.Fatalf("ResumeTransitionAllowed(running, leased): %v, want nil", err)
	}
	for _, to := range []JobState{JobStatePending, JobStateRunning, JobStateVerifying, JobStateFailed} {
		if err := ResumeTransitionAllowed(JobStateRunning, to); err == nil {
			t.Fatalf("ResumeTransitionAllowed(running, %s): err = nil, want refusal", to)
		}
	}
	if err := ResumeTransitionAllowed(JobStateLeased, JobStateRunning); err == nil {
		t.Fatal("ResumeTransitionAllowed(leased, running): err = nil, want refusal (not a resume edge)")
	}
	if err := ResumeTransitionAllowed(JobState("bogus"), JobStateLeased); err == nil {
		t.Fatal("ResumeTransitionAllowed(bogus, leased): err = nil, want refusal on unknown from-state")
	}
	if err := ResumeTransitionAllowed(JobStateRunning, JobState("bogus")); err == nil {
		t.Fatal("ResumeTransitionAllowed(running, bogus): err = nil, want refusal on unknown to-state")
	}
}

// TestPutResumeTransition_RequiresJobID and
// TestPutResumeTransition_RefusesUnknownJob cover PutResumeTransition's
// own input-validation and not-found branches, distinct from the
// precondition and controller-guard paths above.
func TestPutResumeTransition_RequiresJobID(t *testing.T) {
	store := newResumeTestStore(t)
	if err := store.PutResumeTransition(controllerCtx(), "", 10_000); err == nil {
		t.Fatal("PutResumeTransition(\"\"): err = nil, want refusal")
	}
}

func TestPutResumeTransition_RefusesUnknownJob(t *testing.T) {
	store := newResumeTestStore(t)
	if err := store.PutResumeTransition(controllerCtx(), "no-such-job", 10_000); err == nil {
		t.Fatal("PutResumeTransition(no-such-job): err = nil, want KindNotFound refusal")
	}
}
