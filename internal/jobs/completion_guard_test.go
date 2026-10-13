package jobs

// Purpose: CompletionPolicy.commit's from-state guard (P1-SEC-21): the
//
//	UPDATE carries the evaluated state and must change exactly one row, so
//	two transitions evaluated from one state cannot both win, and a job
//	moved out of band after evaluation is left untouched.
//
// Constraints: stored state is asserted, never events; no sleeps.
// SPORT: jobs/completion-gate/ADD (tests) (P1-SEC-21).

import (
	"context"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func storedJobState(t *testing.T, store *Store, id string) JobState {
	t.Helper()
	job, ok, err := store.GetJob(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s) = %v, %v", id, ok, err)
	}
	return job.State
}

// setJobState moves the stored row AND the caller's copy to state, so the
// evaluated state a Transition presents matches what the table holds.
func setJobState(t *testing.T, store *Store, job *Job, state JobState) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `UPDATE `+tableJob+` SET state = ? WHERE id = ?`,
		string(state), job.ID); err != nil {
		t.Fatalf("set stored state %s: %v", state, err)
	}
	job.State = state
}

func commitReq(job Job, target JobState) TransitionRequest {
	return TransitionRequest{Job: &job, Target: target, Caller: PolicyEngineIdentity{EngineID: testEngineID}}
}

func TestCompletionConcurrentTransitionOneWins(t *testing.T) {
	cp, store, _, _, job := newCompletionFixture(t, RiskClassNormal)
	ctx := context.Background()
	targets := []JobState{JobStateAccepted, JobStateFailed}
	errs := make([]error, len(targets))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = cp.commit(ctx, commitReq(job, target), 0)
		}()
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner < 0:
			winner = i
		case err == nil:
			t.Fatalf("both transitions committed from %s: errs = %v", job.State, errs)
		case !cascade.HasKind(err, cascade.KindConflict):
			t.Fatalf("loser error = %v, want KindConflict", err)
		}
	}
	if winner < 0 {
		t.Fatalf("no transition committed: errs = %v", errs)
	}
	if got := storedJobState(t, store, job.ID); got != targets[winner] {
		t.Fatalf("stored state = %s, want the winner's %s", got, targets[winner])
	}
}

func TestCompletionStaleFromStateRefused(t *testing.T) {
	cp, store, _, _, job := newCompletionFixture(t, RiskClassNormal)
	ctx := context.Background()
	stale := job
	setJobState(t, store, &stale, JobStateCancelling) // out of band: job still says running
	err := cp.commit(ctx, commitReq(job, JobStateVerifying), 0)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("commit from a stale state = %v, want KindConflict", err)
	}
	if got := storedJobState(t, store, job.ID); got != JobStateCancelling {
		t.Fatalf("stored state = %s, want the out-of-band %s untouched", got, JobStateCancelling)
	}
	if job.State != JobStateRunning {
		t.Fatalf("the caller's evaluated state = %s, want it left at running", job.State)
	}
}

// TestCompletionCommitFromEvaluatedStateApplies guards the guard: the same
// commit with a matching state still applies, so the refusal above is the
// state mismatch and not a commit that always fails.
func TestCompletionCommitFromEvaluatedStateApplies(t *testing.T) {
	cp, store, _, _, job := newCompletionFixture(t, RiskClassNormal)
	req := commitReq(job, JobStateVerifying)
	if err := cp.commit(context.Background(), req, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := storedJobState(t, store, job.ID); got != JobStateVerifying {
		t.Fatalf("stored state = %s, want verifying", got)
	}
	if req.Job.State != JobStateVerifying {
		t.Fatalf("request job state = %s, want verifying after a commit", req.Job.State)
	}
}
