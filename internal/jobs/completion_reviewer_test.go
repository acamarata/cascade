package jobs

// Purpose: exercise completion reviewer tree, lease and independence gates.
// SPORT: jobs/completion-reviewer/TEST (P1-PBD-01h).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

type completionReviewPolicy struct{ distinct, hold bool }

// RequiresDistinctFamily supplies the test policy's independence requirement.
func (p completionReviewPolicy) RequiresDistinctFamily() bool { return p.distinct }

// HoldsWithoutDistinctFamily supplies the test policy's refusal requirement.
func (p completionReviewPolicy) HoldsWithoutDistinctFamily() bool { return p.hold }

type completionReviewerInputs struct {
	authors                []FamilyRef
	reviewer               FamilyRef
	authorErr, reviewerErr error
	jobID                  string
}

// AuthorFamilies returns the observed authors and captures the requested job.
func (in *completionReviewerInputs) AuthorFamilies(_ context.Context, id string) ([]FamilyRef, error) {
	in.jobID = id
	return in.authors, in.authorErr
}

// ReviewerFamily checks the job binding and returns the observed reviewer.
func (in *completionReviewerInputs) ReviewerFamily(_ context.Context, id string) (FamilyRef, error) {
	if id != in.jobID {
		panic("reviewer input job differs from author input job")
	}
	return in.reviewer, in.reviewerErr
}

func reviewerCompletionFixture(t *testing.T) (*CompletionPolicy, *Store, TransitionRequest) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cp, store, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
	if err := ApplyReviewerSchema(context.Background(), store.db, migrate.SQLiteEmitter{}, cp.deps.Clock, "", ""); err != nil {
		t.Fatal(err)
	}
	job.MinTaskClass, job.MutableScope = "review", ""
	if err := store.PutJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	setJobState(t, store, &job, JobStateReviewing)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	cp.deps.Reviewer = &ReviewerEnforcement{
		Inputs:    &completionReviewerInputs{authors: []FamilyRef{{Family: "anthropic", Resolved: true}}, reviewer: FamilyRef{Family: "openai", Resolved: true}},
		Policy:    func(RiskClass) (ReviewPolicySource, error) { return completionReviewPolicy{true, false}, nil },
		Decisions: store,
	}
	return cp, store, TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassNormal, CheckpointID: "cp-1", TreeHash: "tree-1",
		ReviewerTreeAtCompletion: "tree-1", PolicyVersion: "pv-1",
	}
}

func reviewerDenial(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, cause error) *ErrorCompletionDenied {
	t.Helper()
	err := cp.Transition(context.Background(), req)
	var denied *ErrorCompletionDenied
	if !errors.As(err, &denied) || denied.Cause != cause || (cause != nil && !errors.Is(err, cause)) {
		t.Fatalf("Transition = %#v, want denial with cause %v", err, cause)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateReviewing || req.Job.State != JobStateReviewing {
		t.Fatal("denial changed reviewing state")
	}
	if countGateEvents(t, cp.deps.Bus) != 1 {
		t.Fatal("want exactly one gate denial event")
	}
	return denied
}

func reviewerRow(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, outcome string, distinct, degraded bool) {
	t.Helper()
	rows, err := store.ReviewerDecisions(context.Background(), req.Job.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("decisions = %v, %v", rows, err)
	}
	d := rows[0]
	if d.JobID != req.Job.ID || d.Phase != ReviewPhaseCompletion || d.RiskClass != string(req.PlannedRiskClass) ||
		d.Outcome != outcome || d.FamilyDistinct != distinct || d.Degraded != degraded || !d.DecidedAt.Equal(cp.deps.Clock.Now().UTC()) {
		t.Fatalf("unexpected decision: %+v", d)
	}
}

// TestCompletionRejectsMutatedReviewerWorktree binds acceptance to a read-only review.
func TestCompletionRejectsMutatedReviewerWorktree(t *testing.T) {
	for _, mutable := range []bool{false, true} {
		t.Run(map[bool]string{false: "tree", true: "lease"}[mutable], func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			cause := error(ErrReviewerMutatedWorktree)
			if mutable {
				req.Job.MutableScope = "src/**"
				cause = ErrMutableReview
			} else {
				req.ReviewerTreeAtCompletion = "tree-2"
			}
			d := reviewerDenial(t, cp, store, req, cause)
			if d.Reason != "reviewer denied-mutated-worktree: "+cause.Error() {
				t.Fatal(d.Reason)
			}
			reviewerRow(t, cp, store, req, ReviewOutcomeDeniedMutated, false, false)
		})
	}
	cp, store, req := reviewerCompletionFixture(t)
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("clean review was not accepted")
	}
	reviewerRow(t, cp, store, req, ReviewOutcomeAssigned, true, false)
	rows, _ := store.ReviewerDecisions(context.Background(), req.Job.ID)
	in := cp.deps.Reviewer.Inputs.(*completionReviewerInputs)
	if in.jobID != req.Job.ID || !reflect.DeepEqual(rows[0].Authors, in.authors) || rows[0].Reviewer != in.reviewer {
		t.Fatal("observed input binding differs")
	}
}

// TestCompletionRejectsMissingReviewerTree refuses either unknown tree.
func TestCompletionRejectsMissingReviewerTree(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "after", true: "before"}[before], func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			if before {
				req.TreeHash = ""
			} else {
				req.ReviewerTreeAtCompletion = ""
			}
			_ = reviewerDenial(t, cp, store, req, ErrReviewerMutatedWorktree)
			reviewerRow(t, cp, store, req, ReviewOutcomeDeniedMutated, false, false)
		})
	}
}

// TestCompletionHoldsDegradedIndependence checks the real approval token and hold ladder.
func TestCompletionHoldsDegradedIndependence(t *testing.T) {
	cp, store, req := reviewerCompletionFixture(t)
	cp.deps.Reviewer.Inputs.(*completionReviewerInputs).reviewer = FamilyRef{Family: "anthropic", Resolved: true}
	d := reviewerDenial(t, cp, store, req, ErrHumanApprovalRequired)
	if !d.ExpiredApproval {
		t.Fatal("missing approval did not expire")
	}
	if !strings.HasPrefix(d.Reason, ErrHumanApprovalRequired.Error()+": ") {
		t.Fatal(d.Reason)
	}
	reviewerRow(t, cp, store, req, ReviewOutcomeDegraded, false, true)
	queue, mint := newApprovalQueueForTest(t)
	cp.deps.Approvals = queue
	req.ApprovalRequestID, req.ApprovalNonce = mint(req.Job.ID, "tree-1", "pv-1")
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("approved degradation not accepted")
	}
	reviewerRow(t, cp, store, req, ReviewOutcomeDegraded, false, true)
	t.Run("high", func(t *testing.T) {
		cp, store, req := reviewerCompletionFixture(t)
		req.PlannedRiskClass = RiskClassHigh
		cp.deps.Reviewer.Inputs.(*completionReviewerInputs).reviewer = FamilyRef{Family: "anthropic", Resolved: true}
		cp.deps.Reviewer.Policy = func(rc RiskClass) (ReviewPolicySource, error) {
			if rc != RiskClassHigh {
				t.Fatalf("policy risk = %s", rc)
			}
			return completionReviewPolicy{true, true}, nil
		}
		d := reviewerDenial(t, cp, store, req, ErrNoDistinctFamily)
		if d.Reason != "reviewer held: "+ErrNoDistinctFamily.Error() {
			t.Fatal(d.Reason)
		}
		reviewerRow(t, cp, store, req, ReviewOutcomeHeld, false, false)
	})
}

// TestCompletionReviewerEnforcementUnset requires collaborators only for review jobs.
func TestCompletionReviewerEnforcementUnset(t *testing.T) {
	for _, field := range []string{"reviewer", "inputs", "policy", "decisions"} {
		t.Run(field, func(t *testing.T) {
			cp, store, req := reviewerCompletionFixture(t)
			switch field {
			case "reviewer":
				cp.deps.Reviewer = nil
			case "inputs":
				cp.deps.Reviewer.Inputs = nil
			case "policy":
				cp.deps.Reviewer.Policy = nil
			case "decisions":
				cp.deps.Reviewer.Decisions = nil
			}
			d := reviewerDenial(t, cp, store, req, nil)
			if d.Reason != "reviewer enforcement not configured" {
				t.Fatal(d.Reason)
			}
			rows, err := store.ReviewerDecisions(context.Background(), req.Job.ID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("unexpected rows: %v, %v", rows, err)
			}
		})
	}
	cp, store, req := reviewerCompletionFixture(t)
	cp.deps.Reviewer, req.Job.MinTaskClass = nil, "code"
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("code job not accepted")
	}
}
