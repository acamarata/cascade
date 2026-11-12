package jobs

// Purpose: verify reviewer dependency failures and ordering before acceptance.
// SPORT: jobs/completion-reviewer/TEST (P1-PBD-01h).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type reviewerDecisionFailure struct{ err error }

// RecordReviewerDecision injects a decision persistence failure.
func (f reviewerDecisionFailure) RecordReviewerDecision(context.Context, ReviewerDecision) (ReviewerDecision, error) {
	return ReviewerDecision{}, f.err
}

// TestCompletionReviewerUnknownTaskClassDenies refuses misspelled task classes.
func TestCompletionReviewerUnknownTaskClassDenies(t *testing.T) {
	for _, class := range []string{"reviewer", "code", ""} {
		t.Run(class, func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			req.Job.MinTaskClass = class
			err := cp.Transition(context.Background(), req)
			if class != "reviewer" {
				if err != nil || storedJobState(t, store, req.Job.ID) != JobStateAccepted {
					t.Fatalf("known class: %v", err)
				}
				return
			}
			var denied *ErrorCompletionDenied
			if !errors.As(err, &denied) || !cascade.HasKind(denied.Cause, cascade.KindInvalidInput) ||
				denied.Reason != "reviewer denied-unreadable: "+denied.Cause.Error() ||
				!strings.Contains(denied.Cause.Error(), "jobs: unknown min task class \"reviewer\"") {
				t.Fatalf("unknown class = %v", err)
			}
			if countGateEvents(t, cp.deps.Bus) != 1 || storedJobState(t, store, req.Job.ID) != JobStateReviewing {
				t.Fatal("unknown class was not held")
			}
			reviewerRow(t, cp, store, req, ReviewOutcomeDeniedUnreadable, false, false)
		})
	}
}

// TestCompletionReviewerErrorsDeny preserves each injected error by identity.
func TestCompletionReviewerErrorsDeny(t *testing.T) {
	for _, source := range []string{"authors", "reviewer", "policy", "decisions", "control"} {
		t.Run(source, func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			injected := errors.New(source + " unavailable")
			in := cp.deps.Reviewer.Inputs.(*completionReviewerInputs)
			switch source {
			case "authors":
				in.authorErr = injected
			case "reviewer":
				in.reviewerErr = injected
			case "policy":
				cp.deps.Reviewer.Policy = func(RiskClass) (ReviewPolicySource, error) { return nil, injected }
			case "decisions":
				cp.deps.Reviewer.Decisions = reviewerDecisionFailure{injected}
			case "control":
				if err := cp.Transition(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
					t.Fatal("control not accepted")
				}
				return
			}
			d := reviewerDenial(t, cp, store, req, injected)
			if !strings.Contains(d.Reason, injected.Error()) {
				t.Fatal(d.Reason)
			}
			if source == "decisions" {
				if d.Reason != "reviewer decision not recorded: "+injected.Error() {
					t.Fatal(d.Reason)
				}
				return
			}
			reviewerRow(t, cp, store, req, ReviewOutcomeDeniedUnreadable, source == "reviewer", false)
		})
	}
}

// TestCompletionReviewerRunsBeforeAcceptance avoids CI runs for invalid reviews.
func TestCompletionReviewerRunsBeforeAcceptance(t *testing.T) {
	cp, store, req := reviewerCompletionFixture(t)
	calls := 0
	cp.deps.RequireAcceptance = true
	cp.deps.Acceptance = acceptanceVerifierFunc(func(_ context.Context, id string) (AcceptanceVerdict, error) {
		calls++
		if id != req.Job.ID {
			t.Fatalf("acceptance job = %s", id)
		}
		return AcceptanceVerdict{Passed: true, CheckpointID: "cp-1", TreeHash: "tree-1"}, nil
	})
	req.ReviewerTreeAtCompletion = "tree-2"
	_ = reviewerDenial(t, cp, store, req, ErrReviewerMutatedWorktree)
	if calls != 0 {
		t.Fatal("acceptance ran on mutated review")
	}
	req.ReviewerTreeAtCompletion = "tree-1"
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("clean review did not run acceptance once")
	}
}

// TestCompletionReviewerObservedFamilies checks every author and unknown observations.
func TestCompletionReviewerObservedFamilies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		authors  []FamilyRef
		reviewer FamilyRef
	}{
		{name: "missing authors", reviewer: FamilyRef{Family: "openai", Resolved: true}},
		{name: "unresolved author", authors: []FamilyRef{{Family: "anthropic"}}, reviewer: FamilyRef{Family: "openai", Resolved: true}},
		{name: "unresolved reviewer", authors: []FamilyRef{{Family: "anthropic", Resolved: true}}, reviewer: FamilyRef{Family: "openai"}},
		{name: "second author matches", authors: []FamilyRef{{Family: "anthropic", Resolved: true}, {Family: "openai", Resolved: true}}, reviewer: FamilyRef{Family: "openai", Resolved: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			in := cp.deps.Reviewer.Inputs.(*completionReviewerInputs)
			in.authors, in.reviewer = tc.authors, tc.reviewer
			_ = reviewerDenial(t, cp, store, req, ErrHumanApprovalRequired)
			reviewerRow(t, cp, store, req, ReviewOutcomeDegraded, false, true)
		})
	}
}
