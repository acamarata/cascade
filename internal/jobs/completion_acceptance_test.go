package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type acceptanceVerifierFunc func(context.Context, string) (AcceptanceVerdict, error)

func (fn acceptanceVerifierFunc) VerifyAcceptance(ctx context.Context, id string) (AcceptanceVerdict, error) {
	return fn(ctx, id)
}

func acceptanceCompletionFixture(t *testing.T) (*CompletionPolicy, *Store, TransitionRequest) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cp, store, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
	setJobState(t, store, &job, JobStateReviewing)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	cp.deps.RequireAcceptance = true
	return cp, store, TransitionRequest{
		Job: &job, Target: JobStateAccepted, PlannedRiskClass: RiskClassNormal,
		Caller: PolicyEngineIdentity{EngineID: testEngineID}, CheckpointID: "cp-1", TreeHash: "tree",
	}
}

func TestAcceptedRefusedWhenAcceptanceRerunNotPassed(t *testing.T) {
	cases := []struct {
		name, checkpoint, tree string
		passed                 bool
		err                    error
		emptyRequest           bool
	}{
		{name: "failed", checkpoint: "cp-1", tree: "tree"},
		{name: "error", passed: true, checkpoint: "cp-1", tree: "tree", err: errors.New("verifier unavailable")},
		{name: "wrong checkpoint", passed: true, checkpoint: "old", tree: "tree"},
		{name: "wrong tree", passed: true, checkpoint: "cp-1", tree: "other"},
		{name: "empty request", passed: true, checkpoint: "", tree: "tree", emptyRequest: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp, store, req := acceptanceCompletionFixture(t)
			cp.deps.Acceptance = acceptanceVerifierFunc(func(context.Context, string) (AcceptanceVerdict, error) {
				return AcceptanceVerdict{Passed: tc.passed, CheckpointID: tc.checkpoint, TreeHash: tc.tree}, tc.err
			})
			if tc.emptyRequest {
				req.CheckpointID = ""
			}
			assertAcceptanceDenied(t, cp, store, req)
		})
	}
}

func assertAcceptanceDenied(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest) {
	t.Helper()
	err := cp.Transition(context.Background(), req)
	var denied *ErrorCompletionDenied
	if !errors.As(err, &denied) || denied.Reason != "ci-acceptance-not-passed" {
		t.Fatalf("Transition = %v, want acceptance denial", err)
	}
	if state := storedJobState(t, store, req.Job.ID); state != JobStateReviewing {
		t.Fatalf("stored state = %s, want reviewing", state)
	}
	if n := countGateEvents(t, cp.deps.Bus); n != 1 {
		t.Fatalf("gate denied events = %d, want 1", n)
	}
	// A passed re-run of the same stored checkpoint is the positive control.
	cp.deps.Acceptance = acceptanceVerifierFunc(func(context.Context, string) (AcceptanceVerdict, error) {
		return AcceptanceVerdict{Passed: true, CheckpointID: "cp-1", TreeHash: "tree"}, nil
	})
	req.CheckpointID, req.TreeHash = "cp-1", "tree"
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	if state := storedJobState(t, store, req.Job.ID); state != JobStateAccepted {
		t.Fatalf("positive stored state = %s, want accepted", state)
	}
}

func TestAcceptedAllowedWhenAcceptanceRerunPassed(t *testing.T) {
	cp, store, req := acceptanceCompletionFixture(t)
	calls := 0
	cp.deps.Acceptance = acceptanceVerifierFunc(func(_ context.Context, id string) (AcceptanceVerdict, error) {
		calls++
		if id != req.Job.ID {
			t.Fatalf("verifier job = %q, want %q", id, req.Job.ID)
		}
		return AcceptanceVerdict{Passed: true, CheckpointID: req.CheckpointID, TreeHash: req.TreeHash}, nil
	})
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if state := storedJobState(t, store, req.Job.ID); state != JobStateAccepted {
		t.Fatalf("stored state = %s, want accepted", state)
	}
	if calls != 1 || countGateEvents(t, cp.deps.Bus) != 0 {
		t.Fatalf("verifier calls = %d or unexpected gate denial", calls)
	}
}

func TestCompletionPolicyRequiresVerifierWhenRequired(t *testing.T) {
	cp, _, _ := acceptanceCompletionFixture(t)
	if _, err := NewCompletionPolicy(cp.deps); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("missing required verifier = %v, want invalid input", err)
	}
	cp.deps.RequireAcceptance = false
	if _, err := NewCompletionPolicy(cp.deps); err != nil {
		t.Fatalf("evidence-only construction: %v", err)
	}
}
