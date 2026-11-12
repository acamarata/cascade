package jobs

// Purpose: bind accepted review jobs to unchanged trees and observed families.
// Inputs: completion request, derived risk and injected reviewer collaborators.
// Outputs: a recorded completion decision and, on refusal, one gate denial.
// Constraints: reuse the reviewer family policy and checkpoint-bound approvals.
// SPORT: jobs/completion-reviewer/ADD (P1-PBD-01h).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ReviewerInputs supplies observed author and reviewer families for a review job.
type ReviewerInputs interface {
	AuthorFamilies(ctx context.Context, reviewJobID string) ([]FamilyRef, error)
	ReviewerFamily(ctx context.Context, reviewJobID string) (FamilyRef, error)
}

// ReviewerEnforcement binds reviewer observations, risk policy and decision storage.
type ReviewerEnforcement struct {
	Inputs    ReviewerInputs
	Policy    func(rc RiskClass) (ReviewPolicySource, error)
	Decisions interface {
		RecordReviewerDecision(ctx context.Context, d ReviewerDecision) (ReviewerDecision, error)
	}
}

func (cp *CompletionPolicy) checkReviewer(ctx context.Context, req TransitionRequest, derived RiskClass) error {
	if req.Target != JobStateAccepted {
		return nil
	}
	d := ReviewerDecision{JobID: req.Job.ID, Phase: ReviewPhaseCompletion,
		RiskClass: string(derived), DecidedAt: cp.deps.Clock.Now().UTC()}
	if class := req.Job.MinTaskClass; class != "" && !knownCompletionTaskClass(class) {
		cause := cascade.Newf(cascade.KindInvalidInput, "jobs: unknown min task class %q", class)
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedUnreadable, cause, false)
	}
	if req.Job.MinTaskClass != string(conductor.TaskClassReview) {
		return nil
	}
	en := cp.deps.Reviewer
	if en == nil || en.Inputs == nil || en.Policy == nil || en.Decisions == nil {
		return cp.deny(ctx, req, &ErrorCompletionDenied{Reason: "reviewer enforcement not configured"})
	}
	var mutable []string
	if req.Job.MutableScope != "" {
		mutable = strings.Split(req.Job.MutableScope, ",")
	}
	if err := EnforceReadOnlyLease(TemplateKindReview, DagNode{MutableScope: mutable}); err != nil {
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedMutated, err, false)
	}
	if err := VerifyReadOnly(req.TreeHash, req.ReviewerTreeAtCompletion); err != nil {
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedMutated, err, false)
	}
	policy, err := en.Policy(derived)
	if err != nil {
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedUnreadable, err, false)
	}
	d.Authors, err = en.Inputs.AuthorFamilies(ctx, req.Job.ID)
	if err != nil {
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedUnreadable, err, false)
	}
	d.Reviewer, err = en.Inputs.ReviewerFamily(ctx, req.Job.ID)
	if err != nil {
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDeniedUnreadable, err, false)
	}
	return cp.checkReviewerFamilies(ctx, req, d, policy)
}

func knownCompletionTaskClass(class string) bool {
	for _, row := range conductor.TaskClasses() {
		if row.Class == class {
			return true
		}
	}
	return false
}

func (cp *CompletionPolicy) checkReviewerFamilies(ctx context.Context, req TransitionRequest, d ReviewerDecision, policy ReviewPolicySource) error {
	authors := d.Authors
	if len(authors) == 0 {
		authors = []FamilyRef{{}}
	}
	for _, author := range authors {
		degraded, err := VerifyObservedFamily(policy, author, d.Reviewer)
		if err != nil {
			return cp.finishReviewer(ctx, req, d, ReviewOutcomeHeld, ErrNoDistinctFamily, false)
		}
		d.Degraded = d.Degraded || degraded
	}
	if d.Degraded {
		if err := cp.checkApproval(ctx, req); err != nil {
			return cp.finishReviewer(ctx, req, d, ReviewOutcomeDegraded, err, true)
		}
		return cp.finishReviewer(ctx, req, d, ReviewOutcomeDegraded, nil, false)
	}
	return cp.finishReviewer(ctx, req, d, ReviewOutcomeAssigned, nil, false)
}

func (cp *CompletionPolicy) finishReviewer(ctx context.Context, req TransitionRequest, d ReviewerDecision, outcome string, cause error, expired bool) error {
	d.Outcome = outcome
	d.FamilyDistinct = len(d.Authors) > 0
	for _, author := range d.Authors {
		d.FamilyDistinct = d.FamilyDistinct && familiesDistinct(author, d.Reviewer)
	}
	if en := cp.deps.Reviewer; en != nil && en.Decisions != nil {
		if _, err := en.Decisions.RecordReviewerDecision(ctx, d); err != nil {
			return cp.deny(ctx, req, &ErrorCompletionDenied{Reason: "reviewer decision not recorded: " + err.Error(), Cause: err})
		}
	}
	if cause == nil {
		return nil
	}
	denial := &ErrorCompletionDenied{Cause: cause, Reason: "reviewer " + outcome + ": " + cause.Error()}
	if expired {
		denial.Cause = ErrHumanApprovalRequired
		denial.ExpiredApproval = true
		denial.Reason = ErrHumanApprovalRequired.Error() + ": " + cause.Error()
	}
	return cp.deny(ctx, req, denial)
}
