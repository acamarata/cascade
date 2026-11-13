//go:build integration

package jobs

// Purpose: prove the completion reviewer checks run in their documented
// order on one real SQLite store: tree binding, then family hold, then the
// degraded approval, then the decision row the review evidence points at.
// SPORT: jobs/completion-reviewer/TEST.

import (
	"context"
	"errors"
	"testing"
)

// integrationReviewPolicy holds on High and degrades below it.
func integrationReviewPolicy(rc RiskClass) (ReviewPolicySource, error) {
	return completionReviewPolicy{distinct: true, hold: rc == RiskClassHigh || rc == RiskClassCritical}, nil
}

// newReviewerRow transitions req and returns the one decision row it added.
func newReviewerRow(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, seen map[string]bool) (ReviewerDecision, error) {
	t.Helper()
	err := cp.Transition(context.Background(), req)
	rows, lerr := store.ReviewerDecisions(context.Background(), req.Job.ID)
	if lerr != nil {
		t.Fatalf("ReviewerDecisions: %v", lerr)
	}
	var added []ReviewerDecision
	for _, row := range rows {
		if !seen[row.ID] {
			seen[row.ID] = true
			added = append(added, row)
		}
	}
	if len(added) != 1 {
		t.Fatalf("Transition added %d decision rows, want 1: %+v", len(added), added)
	}
	return added[0], err
}

// wantReviewerDenial asserts a denial with cause, an unchanged reviewing
// state and the cumulative gate denial count.
func wantReviewerDenial(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, err, cause error, denials int) {
	t.Helper()
	var denied *ErrorCompletionDenied
	if !errors.As(err, &denied) || denied.Cause != cause || !errors.Is(err, cause) {
		t.Fatalf("Transition = %#v, want denial with cause %v", err, cause)
	}
	if denied.Cause.Error() != cause.Error() {
		t.Fatalf("cause message = %q, want %q", denied.Cause.Error(), cause.Error())
	}
	if storedJobState(t, store, req.Job.ID) != JobStateReviewing {
		t.Fatal("denial moved the job out of reviewing")
	}
	if got := countGateEvents(t, cp.deps.Bus); got != denials {
		t.Fatalf("gate denials = %d, want %d", got, denials)
	}
}

// TestIntegrationCompletionReviewerPolicy walks one review job through
// every reviewer check on a real store. Each step leaves the later
// failures in place, so the outcome proves which check runs first.
func TestIntegrationCompletionReviewerPolicy(t *testing.T) {
	cp, store, req := reviewerCompletionFixture(t)
	cp.deps.Reviewer.Policy = integrationReviewPolicy
	in := cp.deps.Reviewer.Inputs.(*completionReviewerInputs)
	in.reviewer = FamilyRef{Family: "anthropic", Resolved: true}
	seen := map[string]bool{}

	// Mutated tree, same family, High risk and no approval: the tree wins.
	req.PlannedRiskClass, req.ReviewerTreeAtCompletion = RiskClassHigh, "tree-2"
	row, err := newReviewerRow(t, cp, store, req, seen)
	wantReviewerDenial(t, cp, store, req, err, ErrReviewerMutatedWorktree, 1)
	wantOutcome(t, row, ReviewOutcomeDeniedMutated, RiskClassHigh, false)
	if in.jobID != "" {
		t.Fatal("families were read before the tree check refused")
	}

	// Tree restored, still High with one family: the hold wins over approval.
	req.ReviewerTreeAtCompletion = req.TreeHash
	row, err = newReviewerRow(t, cp, store, req, seen)
	wantReviewerDenial(t, cp, store, req, err, ErrNoDistinctFamily, 2)
	wantOutcome(t, row, ReviewOutcomeHeld, RiskClassHigh, false)

	// Normal with one family degrades and needs a human approval.
	req.PlannedRiskClass = RiskClassNormal
	row, err = newReviewerRow(t, cp, store, req, seen)
	wantReviewerDenial(t, cp, store, req, err, ErrHumanApprovalRequired, 3)
	wantOutcome(t, row, ReviewOutcomeDegraded, RiskClassNormal, true)

	acceptDegradedReview(t, cp, store, req, row)
}

// acceptDegradedReview consumes an approval token, reaches accepted and
// checks the decision row the review evidence references.
func acceptDegradedReview(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, degraded ReviewerDecision) {
	t.Helper()
	queue, mint := newApprovalQueueForTest(t)
	cp.deps.Approvals = queue
	req.ApprovalRequestID, req.ApprovalNonce = mint(req.Job.ID, req.TreeHash, req.PolicyVersion)
	if err := cp.Transition(context.Background(), req); err != nil {
		t.Fatalf("approved Transition: %v", err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("approved degraded review was not accepted")
	}
	if got := countGateEvents(t, cp.deps.Bus); got != 3 {
		t.Fatalf("acceptance published a gate denial: %d", got)
	}
	rows, err := store.ReviewerDecisions(context.Background(), req.Job.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("decision rows = %d, %v; want 3 (the accepted decision repeats the degraded row)", len(rows), err)
	}
	var accepted ReviewerDecision
	for _, row := range rows {
		if row.Outcome == ReviewOutcomeDegraded {
			accepted = row
		}
	}
	if accepted.ID == "" || accepted.ID != degraded.ID {
		t.Fatalf("accepted decision %q, want the degraded row %q", accepted.ID, degraded.ID)
	}
	ref := ReviewEvidenceRef(accepted)
	if ref != "reviewer-decision:"+accepted.ID {
		t.Fatalf("ReviewEvidenceRef = %q", ref)
	}
	again, err := store.RecordReviewerDecision(context.Background(), accepted)
	if err != nil || ReviewEvidenceRef(again) != ref {
		t.Fatalf("re-recorded ref = %q, %v; want %q", ReviewEvidenceRef(again), err, ref)
	}
}

// wantOutcome checks one completion decision row.
func wantOutcome(t *testing.T, d ReviewerDecision, outcome string, rc RiskClass, degraded bool) {
	t.Helper()
	if d.Phase != ReviewPhaseCompletion || d.Outcome != outcome || d.RiskClass != string(rc) ||
		d.Degraded != degraded || d.FamilyDistinct || d.ID == "" {
		t.Fatalf("decision = %+v, want outcome %s risk %s degraded %v", d, outcome, rc, degraded)
	}
}
