package jobs

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
)

// recordingApprovalQueue is a real ApprovalQueue double (named
// stubless, not Mock*/Fake*/Noop*, per Art.1). Enqueue coalesces every
// request from the SAME subject into ONE lifetime, first-write-wins
// entry -- reproducing "a token issued for an earlier commit cannot be
// replayed against a later one". ConsumeToken only succeeds once
// approved, re-derives/compares Action, and is single-use.
type recordingApprovalQueue struct {
	mu      sync.Mutex
	seq     int
	entries map[string]*recordingEntry
}

// recordingEntry is one queued entry; forceErr, when set, is returned
// verbatim by ConsumeToken, simulating a real queue refusal.
type recordingEntry struct {
	requestID, nonce, action string
	subject                  policy.Subject
	approved, consumed       bool
	forceErr                 error
}

func newRecordingApprovalQueue() *recordingApprovalQueue {
	return &recordingApprovalQueue{entries: map[string]*recordingEntry{}}
}

var _ policy.ApprovalQueue = (*recordingApprovalQueue)(nil)

func (q *recordingApprovalQueue) Enqueue(_ context.Context, req policy.EnqueueRequest) (policy.EnqueueResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.entries {
		if e.subject.Kind == req.Subject.Kind && e.subject.ID == req.Subject.ID {
			return policy.EnqueueResult{RequestID: e.requestID, Deduplicated: true, Token: policy.ApprovalToken{RequestID: e.requestID, Nonce: e.nonce}}, nil
		}
	}
	q.seq++
	id, nonce := "req-"+strconv.Itoa(q.seq), "nonce-"+strconv.Itoa(q.seq)
	q.entries[id] = &recordingEntry{requestID: id, nonce: nonce, subject: req.Subject, action: req.Action}
	return policy.EnqueueResult{RequestID: id, Token: policy.ApprovalToken{RequestID: id, Nonce: nonce}}, nil
}

func (q *recordingApprovalQueue) GetPending(_ context.Context) ([]policy.PendingEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []policy.PendingEntry
	for _, e := range q.entries {
		if !e.approved && !e.consumed {
			out = append(out, policy.PendingEntry{RequestID: e.requestID, ExpiresAt: time.Now().Add(time.Minute)})
		}
	}
	return out, nil
}

func (q *recordingApprovalQueue) Decide(_ context.Context, reqs []policy.DecisionRequest) ([]policy.DecisionOutcome, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]policy.DecisionOutcome, 0, len(reqs))
	for _, r := range reqs {
		e, ok := q.entries[r.RequestID]
		if !ok {
			out = append(out, policy.DecisionOutcome{RequestID: r.RequestID, Err: policy.ErrUnknownRequest})
			continue
		}
		e.approved = r.Approved
		out = append(out, policy.DecisionOutcome{RequestID: r.RequestID})
	}
	return out, nil
}

func (q *recordingApprovalQueue) Cancel(_ context.Context, requestID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.entries, requestID)
	return nil
}

func (q *recordingApprovalQueue) ConsumeToken(_ context.Context, req policy.ConsumeRequest) (policy.ConsumeResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[req.RequestID]
	switch {
	case !ok:
		return policy.ConsumeResult{}, policy.ErrUnknownRequest
	case e.forceErr != nil:
		return policy.ConsumeResult{}, e.forceErr
	case e.consumed:
		return policy.ConsumeResult{}, policy.ErrTokenReplayed
	case req.Nonce != e.nonce, req.Action != e.action:
		return policy.ConsumeResult{}, policy.ErrApprovalMismatch
	case !e.approved:
		return policy.ConsumeResult{}, policy.ErrApprovalNotApproved
	}
	e.consumed = true
	return policy.ConsumeResult{RequestID: e.requestID, Subject: e.subject, ConsumedAt: time.Now().UTC()}, nil
}

func (q *recordingApprovalQueue) Expire(_ context.Context) (int, error) { return 0, nil }

// approve marks id approved directly, bypassing Decide's own binding checks.
func (q *recordingApprovalQueue) approve(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e, ok := q.entries[id]; ok {
		e.approved = true
	}
}

func (q *recordingApprovalQueue) setForceErr(id string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e, ok := q.entries[id]; ok {
		e.forceErr = err
	}
}

func (q *recordingApprovalQueue) onlyID(t *testing.T) string {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) != 1 {
		t.Fatalf("expected exactly one entry, got %d", len(q.entries))
	}
	for id := range q.entries {
		return id
	}
	return ""
}

func humanSubject(id string) policy.Subject { return policy.Subject{Kind: policy.SubjectUser, ID: id} }
func agentSubject(id string) policy.Subject { return policy.Subject{Kind: policy.SubjectAgent, ID: id} }
func criticalNode() DagNode                 { return DagNode{ID: "release-job-1", RiskClass: RiskClassCritical} }

func validRollback(jobID string, producedAt time.Time) RollbackEvidence {
	return RollbackEvidence{
		PreviousReleaseRef: "v1.2.3", RollbackCommand: "cascade release rollback v1.2.3",
		VerificationResult: OutcomePass, ProducedByJobID: jobID, ProducedAt: producedAt,
	}
}

func baseCandidate(jobID string, gateReachedAt time.Time) ReleaseCandidate {
	return ReleaseCandidate{
		JobID: jobID, CommitSHA: "sha-aaa", TreeHash: "tree-aaa", GateSetVersion: "gateset-v1",
		Requester: humanSubject("alice"), GateReachedAt: gateReachedAt,
	}
}

// seedApproved runs one not-yet-approved RequireHumanApproval call, then
// approves the resulting entry -- shared setup for redemption-path tests.
func seedApproved(t *testing.T, q *recordingApprovalQueue, node DagNode, cand ReleaseCandidate, rb RollbackEvidence) {
	t.Helper()
	if _, err := RequireHumanApproval(context.Background(), q, node, cand, rb); !errors.Is(err, ErrReleaseApprovalMissing) {
		t.Fatalf("seed call = %v, want ErrReleaseApprovalMissing", err)
	}
	q.approve(q.onlyID(t))
}

// TestRequireHumanApproval_HappyPath: unapproved fails closed, an
// out-of-band decision approves it, and redemption returns a complete
// human_approval EvidenceRecord naming the verified rollback artifact.
func TestRequireHumanApproval_HappyPath(t *testing.T) {
	gateReachedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cand := baseCandidate("job-1", gateReachedAt)
	rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))
	node := criticalNode()
	q := newRecordingApprovalQueue()
	seedApproved(t, q, node, cand, rb)

	rec, err := RequireHumanApproval(context.Background(), q, node, cand, rb)
	if err != nil {
		t.Fatalf("redemption: unexpected error %v", err)
	}
	if rec.Kind != EvidenceHumanApproval || rec.ProducerCapability != ProducerHumanApproval || rec.Outcome != OutcomePass {
		t.Fatalf("record shape wrong: %+v", rec)
	}
	if rec.JobID != cand.JobID {
		t.Fatalf("JobID = %q, want %q", rec.JobID, cand.JobID)
	}
	if want := rollbackEvidenceArtifactRef(rb); rec.ArtifactRef != want || rec.ArtifactRef == "" {
		t.Fatalf("ArtifactRef = %q, want %q (non-empty)", rec.ArtifactRef, want)
	}
}

// TestRequireHumanApproval_NonCriticalNode: Epic AH's DECIDED
// "Release/CD gate = Critical class" rule, enforced at the call site.
func TestRequireHumanApproval_NonCriticalNode(t *testing.T) {
	gateReachedAt := time.Now().UTC()
	cand := baseCandidate("job-1", gateReachedAt)
	rb := validRollback("build-job-0", gateReachedAt.Add(-time.Hour))
	node := DagNode{ID: "x", RiskClass: RiskClassNormal}
	q := newRecordingApprovalQueue()
	if _, err := RequireHumanApproval(context.Background(), q, node, cand, rb); err == nil {
		t.Fatal("expected an error for a non-Critical node, got nil")
	}
}
