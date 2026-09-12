package jobs

// Split from release_gate_test.go to stay under the 300-line file cap
// (both files share the recordingApprovalQueue double and fixtures
// declared there); these cover the redemption-path properties:
// scope-binding, single-use, queue-error propagation, non-human
// principal refusal, and the pre-queue fail-closed edges.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
)

// TestRequireHumanApproval_ScopeMismatch: a token differing in any one
// of the four bound values is refused with ErrApprovalScopeMismatch,
// including "replayed against a later commit" (the coalesced entry
// redeems against the FIRST candidate's action, not the second's).
func TestRequireHumanApproval_ScopeMismatch(t *testing.T) {
	gateReachedAt := time.Now().UTC()
	producedAt := gateReachedAt.Add(-time.Hour)
	rb := validRollback("build-job-0", producedAt)
	node := criticalNode()

	mutate := map[string]func(c *ReleaseCandidate){
		"commit_sha":       func(c *ReleaseCandidate) { c.CommitSHA = "sha-bbb" },
		"tree_hash":        func(c *ReleaseCandidate) { c.TreeHash = "tree-bbb" },
		"gate_set_version": func(c *ReleaseCandidate) { c.GateSetVersion = "gateset-v2" },
		"job_id":           func(c *ReleaseCandidate) { c.JobID = "job-2" },
	}
	for name, mutateFn := range mutate {
		t.Run(name, func(t *testing.T) {
			q := newRecordingApprovalQueue()
			cand1 := baseCandidate("job-1", gateReachedAt)
			seedApproved(t, q, node, cand1, rb)
			cand2 := cand1
			mutateFn(&cand2)
			_, err := RequireHumanApproval(context.Background(), q, node, cand2, validRollback("build-job-0", producedAt))
			if !errors.Is(err, ErrApprovalScopeMismatch) {
				t.Fatalf("mismatched %s: got %v, want ErrApprovalScopeMismatch", name, err)
			}
		})
	}
}

// TestRequireHumanApproval_SingleUse: a second redemption is refused,
// with policy.ErrTokenReplayed reachable through the wrap.
func TestRequireHumanApproval_SingleUse(t *testing.T) {
	gateReachedAt := time.Now().UTC()
	cand := baseCandidate("job-1", gateReachedAt)
	rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))
	node := criticalNode()
	q := newRecordingApprovalQueue()
	seedApproved(t, q, node, cand, rb)
	if _, err := RequireHumanApproval(context.Background(), q, node, cand, rb); err != nil {
		t.Fatalf("first redemption: unexpected error %v", err)
	}
	_, err := RequireHumanApproval(context.Background(), q, node, cand, rb)
	if !errors.Is(err, ErrReleaseApprovalMissing) || !errors.Is(err, policy.ErrTokenReplayed) {
		t.Fatalf("second redemption = %v, want ErrReleaseApprovalMissing wrapping policy.ErrTokenReplayed", err)
	}
}

// TestRequireHumanApproval_QueueErrorsPropagate: forced ErrTokenExpired/
// ErrTokenReplayed both surface as ErrReleaseApprovalMissing, reachable.
func TestRequireHumanApproval_QueueErrorsPropagate(t *testing.T) {
	for _, underlying := range []error{policy.ErrTokenExpired, policy.ErrTokenReplayed} {
		t.Run(underlying.Error(), func(t *testing.T) {
			gateReachedAt := time.Now().UTC()
			cand := baseCandidate("job-1", gateReachedAt)
			rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))
			node := criticalNode()
			q := newRecordingApprovalQueue()
			seedApproved(t, q, node, cand, rb)
			q.setForceErr(q.onlyID(t), underlying)
			_, err := RequireHumanApproval(context.Background(), q, node, cand, rb)
			if !errors.Is(err, ErrReleaseApprovalMissing) || !errors.Is(err, underlying) {
				t.Fatalf("got %v, want ErrReleaseApprovalMissing wrapping %v", err, underlying)
			}
		})
	}
}

// TestRequireHumanApproval_NonHumanPrincipal: a non-SubjectUser redeemed
// token is refused with ErrNonHumanApprovalPrincipal -- SubjectAgent
// stands in for both a plain agent and "no autonomy preset" (the
// contract's driver/node/daemon spellings name no real SubjectKind).
func TestRequireHumanApproval_NonHumanPrincipal(t *testing.T) {
	gateReachedAt := time.Now().UTC()
	cand := baseCandidate("job-1", gateReachedAt)
	cand.Requester = agentSubject("autonomy-preset-balanced")
	rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))
	node := criticalNode()
	q := newRecordingApprovalQueue()
	seedApproved(t, q, node, cand, rb)
	_, err := RequireHumanApproval(context.Background(), q, node, cand, rb)
	if !errors.Is(err, ErrNonHumanApprovalPrincipal) {
		t.Fatalf("got %v, want ErrNonHumanApprovalPrincipal", err)
	}
}

// TestRequireHumanApproval_PreQueueRefusals covers the fail-closed edges
// hit before the queue is ever touched: a nil collaborator and
// unverifiable rollback evidence (which must never even reach Enqueue).
func TestRequireHumanApproval_PreQueueRefusals(t *testing.T) {
	gateReachedAt := time.Now().UTC()
	cand := baseCandidate("job-1", gateReachedAt)
	rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))

	t.Run("nil_queue", func(t *testing.T) {
		if _, err := RequireHumanApproval(context.Background(), nil, criticalNode(), cand, rb); err == nil {
			t.Fatal("expected an error for a nil approval queue, got nil")
		}
	})
	t.Run("bad_rollback", func(t *testing.T) {
		q := newRecordingApprovalQueue()
		_, err := RequireHumanApproval(context.Background(), q, criticalNode(), cand, RollbackEvidence{})
		if !errors.Is(err, ErrMissingRollbackEvidence) {
			t.Fatalf("got %v, want ErrMissingRollbackEvidence", err)
		}
		if len(q.entries) != 0 {
			t.Fatal("unverified rollback evidence must refuse before ever touching the approval queue")
		}
	})
}
