package jobs

// Purpose: the Critical-class completion approval accepts only a token
// filed to run as a human user; an agent, plugin or unnamed principal is
// refused with ErrNonHumanApprovalPrincipal, and a queue refusal passes
// through unchanged. SPORT: jobs/completion-gate/ADD.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
)

// humanApprovalFixture builds a Critical-class completion ready for
// acceptance whose approval token was filed under kind on a fresh
// recordingApprovalQueue and approved. Every leg that redeems a token
// builds its own fixture: the queue is single-use.
func humanApprovalFixture(t *testing.T, kind policy.SubjectKind) (*CompletionPolicy, *Store, TransitionRequest, *recordingApprovalQueue) {
	t.Helper()
	cp, store, req, row := attestedCompletion(t, RiskClassCritical, false)
	passAll(t, cp.deps.Ledger, []EvidenceKind{EvidenceQA})
	appendAttestedEvidence(t, cp, req, row)
	req.PolicyVersion = "pv-1"
	queue := newRecordingApprovalQueue()
	cp.deps.Approvals = queue
	res, err := queue.Enqueue(context.Background(), policy.EnqueueRequest{
		Subject: policy.Subject{Kind: kind, ID: "principal-1"}, Capability: "jobs.complete",
		Level: policy.L2, Action: ApprovalScopeAction(req.Job.ID, req.TreeHash, "pv-1"), Summary: "complete " + req.Job.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	queue.approve(res.RequestID)
	req.ApprovalRequestID, req.ApprovalNonce = res.Token.RequestID, res.Token.Nonce
	return cp, store, req, queue
}

// wantPrincipalRefusal checks err wraps the sentinel by identity (not by
// Kind, which the no-queue refusal shares) and names the quoted kind.
func wantPrincipalRefusal(t *testing.T, err error, kind policy.SubjectKind) {
	t.Helper()
	if err == nil || errors.Unwrap(err) != ErrNonHumanApprovalPrincipal {
		t.Fatalf("checkApproval for a %q token = %v, want a wrap of ErrNonHumanApprovalPrincipal", kind, err)
	}
	for _, want := range []string{fmt.Sprintf("%q", kind), ErrNonHumanApprovalPrincipal.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("checkApproval error %q does not contain %q", err.Error(), want)
		}
	}
}

// wantDeniedReviewing checks Transition folds the principal refusal into
// ExpiredApproval and leaves the job in reviewing.
func wantDeniedReviewing(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest) {
	t.Helper()
	err := cp.Transition(context.Background(), req)
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || !denial.ExpiredApproval || !strings.Contains(denial.Reason, ErrNonHumanApprovalPrincipal.Error()) {
		t.Fatalf("Transition = %v, want ExpiredApproval naming ErrNonHumanApprovalPrincipal", err)
	}
	if got := storedJobState(t, store, req.Job.ID); got != JobStateReviewing {
		t.Fatalf("job state after a refused approval = %s, want reviewing", got)
	}
}

func TestCompletionApprovalRequiresHuman(t *testing.T) {
	for _, kind := range []policy.SubjectKind{policy.SubjectAgent, policy.SubjectPlugin, policy.SubjectKind("")} {
		t.Run("kind="+string(kind), func(t *testing.T) {
			cp, _, req, _ := humanApprovalFixture(t, kind)
			wantPrincipalRefusal(t, cp.checkApproval(context.Background(), req), kind)
			cp, store, req, _ := humanApprovalFixture(t, kind)
			wantDeniedReviewing(t, cp, store, req)
		})
	}

	t.Run("user", func(t *testing.T) {
		cp, _, req, _ := humanApprovalFixture(t, policy.SubjectUser)
		if err := cp.checkApproval(context.Background(), req); err != nil {
			t.Fatalf("checkApproval for a user token = %v, want nil", err)
		}
		cp, store, req, _ := humanApprovalFixture(t, policy.SubjectUser)
		if err := cp.Transition(context.Background(), req); err != nil {
			t.Fatalf("Transition with a user token = %v, want nil", err)
		}
		if got := storedJobState(t, store, req.Job.ID); got != JobStateAccepted {
			t.Fatalf("job state after a user approval = %s, want accepted", got)
		}
	})

	t.Run("queue refusal passes through", func(t *testing.T) {
		cp, _, req, queue := humanApprovalFixture(t, policy.SubjectAgent)
		queue.setForceErr(req.ApprovalRequestID, policy.ErrTokenReplayed)
		err := cp.checkApproval(context.Background(), req)
		if err != policy.ErrTokenReplayed {
			t.Fatalf("checkApproval over a replayed token = %v, want exactly policy.ErrTokenReplayed", err)
		}
		if errors.Unwrap(err) == ErrNonHumanApprovalPrincipal {
			t.Fatal("a queue refusal was turned into the principal error")
		}
	})
}

func TestCompletionApprovalRealQueueAgentSubject(t *testing.T) {
	cp, store, req, row := attestedCompletion(t, RiskClassCritical, false)
	passAll(t, cp.deps.Ledger, []EvidenceKind{EvidenceQA})
	appendAttestedEvidence(t, cp, req, row)
	req.PolicyVersion = "pv-1"
	queue, _ := newApprovalQueueForTest(t)
	cp.deps.Approvals = queue

	ctx := context.Background()
	res, err := queue.Enqueue(ctx, policy.EnqueueRequest{
		Subject: policy.Subject{Kind: policy.SubjectAgent, ID: "lane-a"}, Capability: "jobs.complete",
		Level: policy.L2, Action: ApprovalScopeAction(req.Job.ID, req.TreeHash, "pv-1"), Summary: "complete " + req.Job.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	outcomes, err := queue.Decide(ctx, []policy.DecisionRequest{{
		RequestID: res.RequestID, Approved: true, PresentedSummary: res.Summary, PresentedLevel: res.Level,
	}})
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil {
		t.Fatalf("Decide = %v, %v; want one approved outcome", outcomes, err)
	}
	req.ApprovalRequestID, req.ApprovalNonce = res.Token.RequestID, res.Token.Nonce
	wantDeniedReviewing(t, cp, store, req)
}
