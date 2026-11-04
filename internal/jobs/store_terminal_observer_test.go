package jobs

// Purpose: P1-TOP-10b acceptance for Store.RegisterTerminalObserver: the
// closed outcome mapping after each committed terminal transition, no
// call for non-terminal or refused writes, and the SetTerminalHook lease
// release kept intact when an observer fails.
//
// SPORT: jobs/terminal-observer/ADD (P1-TOP-10b).

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// observation is one ObserveTerminalOutcome call.
type observation struct {
	jobID   string
	outcome TerminalOutcome
}

// recordingObserver records every call and returns err (nil by default).
type recordingObserver struct {
	mu    sync.Mutex
	calls []observation
	err   error
}

func (o *recordingObserver) ObserveTerminalOutcome(_ context.Context, jobID string, outcome TerminalOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, observation{jobID: jobID, outcome: outcome})
	return o.err
}

func (o *recordingObserver) snapshot() []observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observation(nil), o.calls...)
}

// seedJob stores a baseJob(id) in state.
func seedJob(t *testing.T, store *Store, id string, state JobState) {
	t.Helper()
	j := baseJob(id)
	j.State = state
	if err := store.PutJob(context.Background(), j); err != nil {
		t.Fatalf("PutJob(%s): %v", id, err)
	}
}

// policyFinish drives the completion fixture's job-1 from running through
// verifying and reviewing to final via the real CompletionPolicy commit.
func policyFinish(t *testing.T, cp *CompletionPolicy, ledger *EvidenceLedger, job Job, final JobState) {
	t.Helper()
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	caller := PolicyEngineIdentity{EngineID: testEngineID}
	for _, target := range []JobState{JobStateVerifying, JobStateReviewing, final} {
		req := TransitionRequest{Job: &job, Target: target, Caller: caller, PlannedRiskClass: RiskClassNormal}
		if err := cp.Transition(context.Background(), req); err != nil {
			t.Fatalf("Transition to %s: %v", target, err)
		}
	}
}

func wantObservations(t *testing.T, label string, got, want []observation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: observations = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: observation %d = %v, want %v", label, i, got[i], want[i])
		}
	}
}

func TestTerminalObserverMapsOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		final JobState
		want  TerminalOutcome
	}{
		{JobStateAccepted, TerminalOutcomeCompleted},
		{JobStateRejected, TerminalOutcomeFailed},
	} {
		cp, store, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
		obs := &recordingObserver{}
		store.RegisterTerminalObserver(obs)
		policyFinish(t, cp, ledger, job, tc.final)
		wantObservations(t, string(tc.final), obs.snapshot(), []observation{{"job-1", tc.want}})
	}

	store := newTestStore(t)
	obs := &recordingObserver{}
	store.RegisterTerminalObserver(obs)
	seedJob(t, store, "job-failed", JobStateRunning)
	seedJob(t, store, "job-cancelled", JobStateCancelling)
	seedJob(t, store, "job-pending", JobStatePending)

	// A non-terminal commit and three refused writes call no observer.
	if err := store.PutTransition(ctx, "job-pending", JobStateLeased, 2); err != nil {
		t.Fatalf("PutTransition pending->leased: %v", err)
	}
	if err := store.PutTransition(ctx, "job-pending", JobStateCancelled, 3); err == nil {
		t.Fatal("PutTransition leased->cancelled: nil error, want illegal-transition refusal")
	}
	if err := store.PutTransition(ctx, "job-missing", JobStateFailed, 3); err == nil {
		t.Fatal("PutTransition on a missing job: nil error, want not-found")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.PutTransition(cancelled, "job-failed", JobStateFailed, 3); err == nil {
		t.Fatal("PutTransition with a cancelled context: nil error, want a failed commit")
	}
	wantObservations(t, "non-terminal and refused", obs.snapshot(), nil)

	if err := store.PutTransition(ctx, "job-failed", JobStateFailed, 4); err != nil {
		t.Fatalf("PutTransition running->failed: %v", err)
	}
	if err := store.PutTransition(ctx, "job-cancelled", JobStateCancelled, 4); err != nil {
		t.Fatalf("PutTransition cancelling->cancelled: %v", err)
	}
	wantObservations(t, "public terminal", obs.snapshot(), []observation{
		{"job-failed", TerminalOutcomeFailed},
		{"job-cancelled", TerminalOutcomeCancelled},
	})
}

func TestTerminalObserverKeepsLeaseRelease(t *testing.T) {
	ctx := context.Background()
	cp, store, ledger, _, job := newCompletionFixture(t, RiskClassNormal)

	var mu sync.Mutex
	released := map[string]int{}
	var order []string
	store.SetTerminalHook(func(_ context.Context, jobID string) error {
		mu.Lock()
		defer mu.Unlock()
		released[jobID]++
		order = append(order, "release:"+jobID)
		return nil
	})
	obsErr := errors.New("jobs test: observer refused the outcome")
	var sunk []error
	store.SetTerminalObserverErrorSink(func(err error) {
		mu.Lock()
		defer mu.Unlock()
		sunk = append(sunk, err)
		order = append(order, "sink")
	})
	obs := &recordingObserver{err: obsErr}
	store.RegisterTerminalObserver(obs)

	seedJob(t, store, "job-2", JobStateRunning)
	if err := store.PutTransition(ctx, "job-2", JobStateFailed, 2); err != nil {
		t.Fatalf("PutTransition running->failed with a failing observer: %v", err)
	}
	policyFinish(t, cp, ledger, job, JobStateAccepted)

	mu.Lock()
	defer mu.Unlock()
	checkLeaseReleaseOrder(t, released, order, sunk, obsErr)
	wantObservations(t, "observer", obs.snapshot(), []observation{
		{"job-2", TerminalOutcomeFailed},
		{"job-1", TerminalOutcomeCompleted},
	})
}

// checkLeaseReleaseOrder asserts each lease was released exactly once, before
// its observer sink call, and that the sink received the observer's own error.
func checkLeaseReleaseOrder(t *testing.T, released map[string]int, order []string, sunk []error, obsErr error) {
	t.Helper()
	if released["job-1"] != 1 || released["job-2"] != 1 || len(released) != 2 {
		t.Fatalf("lease releases = %v, want exactly once each for job-1 and job-2", released)
	}
	wantOrder := []string{"release:job-2", "sink", "release:job-1", "sink"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v (lease release before observers)", order, wantOrder)
		}
	}
	if len(sunk) != 2 {
		t.Fatalf("sink received %d errors, want 2", len(sunk))
	}
	for i, err := range sunk {
		if err != obsErr || err.Error() != obsErr.Error() {
			t.Fatalf("sink error %d = %v, want the observer's own error", i, err)
		}
	}
}
