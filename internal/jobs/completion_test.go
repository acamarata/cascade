package jobs

// Purpose: CompletionPolicy.Transition's contract: happy path, fail-
// closed/wrong-successor, the done-claim non-effect, the typed denial
// shape, ErrNotPolicyEngine, checkpoint binding, risk escalation, and
// (against the REAL I/S-18.T3 approval queue) the Critical-class
// approval-token binding. SPORT: jobs/completion-gate/ADD (S-60.T3).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/providers/sqlite"
)

const testEngineID = "engine-1"

func newCompletionFixture(t *testing.T, class RiskClass) (*CompletionPolicy, *Store, *EvidenceLedger, *events.Bus, Job) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "completion.db")
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewStore(db)
	job := baseJob("job-1")
	job.RiskClass = string(class)
	job.State = JobStateRunning
	if err := store.PutJob(context.Background(), job); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.PutExecution(context.Background(), Execution{ID: "exec-1", JobID: "job-1", State: ExecutionRunning}); err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	authz := NewProducerAuthz(store, func() bool { return true }, nil)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	ledger, err := NewEvidenceLedger(store, clock, &recordingWriter{}, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	bus := events.New(storetest.NewMemStore(), clock)
	cp, err := NewCompletionPolicy(CompletionPolicyDeps{
		Store: store, Ledger: ledger, Bus: bus, Clock: clock, EngineID: testEngineID,
	})
	if err != nil {
		t.Fatalf("NewCompletionPolicy: %v", err)
	}
	return cp, store, ledger, bus, job
}

func passAll(t *testing.T, ledger *EvidenceLedger, kinds []EvidenceKind) {
	t.Helper()
	for i, k := range kinds {
		rec := passRecord(k, "idem-"+string(k)+"-1")
		if k == EvidenceHumanApproval {
			continue
		}
		if _, err := ledger.Append(context.Background(), rec, AppendAuthorization{ExecutionID: "exec-1"}); err != nil {
			t.Fatalf("seed evidence %d (%s): %v", i, k, err)
		}
	}
}

func countGateEvents(t *testing.T, bus *events.Bus) int {
	t.Helper()
	evs, err := bus.Replay(context.Background(), completionGateNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return len(evs)
}

func TestCompletionHappyPathRunningToAccepted(t *testing.T) {
	cp, store, ledger, bus, job := newCompletionFixture(t, RiskClassNormal)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	ctx := context.Background()
	caller := PolicyEngineIdentity{EngineID: testEngineID}

	for _, target := range []JobState{JobStateVerifying, JobStateReviewing, JobStateAccepted} {
		if err := cp.Transition(ctx, TransitionRequest{Job: &job, Target: target, Caller: caller, PlannedRiskClass: RiskClassNormal}); err != nil {
			t.Fatalf("Transition to %s: %v", target, err)
		}
	}
	got, ok, err := store.GetJob(ctx, "job-1")
	if err != nil || !ok {
		t.Fatalf("GetJob: %v, %v", ok, err)
	}
	if got.State != JobStateAccepted {
		t.Fatalf("job.State = %s, want accepted", got.State)
	}
	if n := countGateEvents(t, bus); n != 0 {
		t.Fatalf("gate.denied events = %d, want 0 on an all-success path", n)
	}
}

func TestCompletionMissingEvidenceDenied(t *testing.T) {
	cp, _, _, bus, job := newCompletionFixture(t, RiskClassNormal)
	ctx := context.Background()
	err := cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: RiskClassNormal,
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || len(denial.MissingEvidence) == 0 {
		t.Fatalf("Transition with no evidence = %v, want *ErrorCompletionDenied{MissingEvidence: non-empty}", err)
	}
	if n := countGateEvents(t, bus); n != 1 {
		t.Fatalf("gate.denied events = %d, want exactly 1", n)
	}
}

func TestCompletionWrongSuccessorDenied(t *testing.T) {
	cp, _, _, _, job := newCompletionFixture(t, RiskClassNormal)
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: RiskClassNormal,
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || denial.WrongSuccessor == nil {
		t.Fatalf("Transition running->accepted = %v, want *ErrorCompletionDenied{WrongSuccessor: non-nil}", err)
	}
}

func TestCompletionUnknownStateFailsClosed(t *testing.T) {
	cp, _, _, _, job := newCompletionFixture(t, RiskClassNormal)
	job.State = JobState("bogus")
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: RiskClassNormal,
	})
	if _, ok := err.(*ErrorCompletionDenied); !ok {
		t.Fatalf("Transition from an unknown state = %v (%T), want *ErrorCompletionDenied", err, err)
	}
}

func TestCompletionNotPolicyEngineWrapper(t *testing.T) {
	cp, _, _, _, job := newCompletionFixture(t, RiskClassNormal)
	// A wrapper that forwards to Transition without the real engine's
	// identity -- the acceptance criterion's own "wrapper that calls
	// Transition without the policy caller identity".
	call := func(id string) error {
		return cp.Transition(context.Background(), TransitionRequest{
			Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: id}, PlannedRiskClass: RiskClassNormal,
		})
	}
	if err := call("not-the-engine"); err != ErrNotPolicyEngine {
		t.Fatalf("call with a wrong engine id = %v, want ErrNotPolicyEngine", err)
	}
	if err := call(""); err != ErrNotPolicyEngine {
		t.Fatalf("call with no engine id = %v, want ErrNotPolicyEngine", err)
	}
}

func TestCompletionDoneClaimDoesNotChangeState(t *testing.T) {
	_, store, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
	ctx := context.Background()
	claim := passRecord(EvidenceBuild, "claim-1")
	claim.ProducerCapability = ProducerAgentClaim
	if _, err := ledger.Append(ctx, claim, AppendAuthorization{ExecutionID: "exec-1"}); err != nil {
		t.Fatalf("Append agent-claim: %v", err)
	}
	got, ok, err := store.GetJob(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: %v, %v", ok, err)
	}
	if got.State != JobStateRunning {
		t.Fatalf("job.State = %s after Append alone, want unchanged (running)", got.State)
	}
}

func TestCompletionCheckpoint(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
	ctx := context.Background()
	for _, k := range []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview} {
		rec := passRecord(k, "idem-cp-"+string(k))
		rec.CheckpointID = "cp-old"
		if _, err := ledger.Append(ctx, rec, AppendAuthorization{ExecutionID: "exec-1"}); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	err := cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassNormal, CheckpointID: "cp-new",
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || denial.CheckpointMismatch == nil {
		t.Fatalf("Transition with a stale checkpoint = %v, want CheckpointMismatch", err)
	}
}

func TestCompletionRiskEscalation(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassLow)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass:   RiskClassLow,
		ActualFootprint:    ChangeFootprint{ChangedPaths: []string{"internal/secrets/vault.go"}},
		LeaseScopePrefixes: []string{"internal/secrets/"},
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || denial.RiskEscalated == nil {
		t.Fatalf("Transition over a secrets-touching footprint at Low = %v, want RiskEscalated", err)
	}
}

// newApprovalQueueForTest builds a real I/S-18.T3 ApprovalQueue over a
// real SQLite store in t.TempDir(), and a mintScope closure that admits
// and approves one {job, tree, policyVersion}-scoped token, returning
// its {RequestID, Nonce}.
func newApprovalQueueForTest(t *testing.T) (*policy.StoreApprovals, func(jobID, tree, ver string) (string, string)) {
	t.Helper()
	ctx := context.Background()
	pdb, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "approvals.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = pdb.Close() })
	reg := policy.NewMemoryRegistry()
	approvalCap := policy.Capability{Name: "jobs.complete", Desc: "complete a job", DefaultPolicy: policy.ClassWorkspaceMutation}
	if err := reg.Add(ctx, approvalCap); err != nil {
		t.Fatalf("register capability: %v", err)
	}
	pclock := runtime.NewFixedClock(time.Now())
	grants, err := policy.NewStoreGrants(pdb, reg, pclock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	queue, err := policy.NewApprovalQueue(policy.ApprovalQueueConfig{Store: pdb, Registry: reg, Grants: grants, Clock: pclock})
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}
	mintScope := func(jobID, tree, ver string) (string, string) {
		res, err := queue.Enqueue(ctx, policy.EnqueueRequest{
			Subject: policy.Subject{Kind: policy.SubjectAgent, ID: "lane-a"}, Capability: approvalCap.Name,
			Level: policy.L2, Action: ApprovalScopeAction(jobID, tree, ver), Summary: "complete " + jobID,
		})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		outcomes, err := queue.Decide(ctx, []policy.DecisionRequest{{
			RequestID: res.RequestID, Approved: true, PresentedSummary: res.Summary, PresentedLevel: res.Level,
		}})
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if outcomes[0].Err != nil {
			t.Fatalf("Decide element refused: %v", outcomes[0].Err)
		}
		return res.Token.RequestID, res.Token.Nonce
	}
	return queue, mintScope
}

func TestCompletionApprovalToken(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassCritical)
	job.State = JobStateReviewing
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial})

	ctx := context.Background()
	queue, mintScope := newApprovalQueueForTest(t)
	cp.deps.Approvals = queue
	var err error

	// A token minted for a DIFFERENT tree hash must be refused.
	reqID, nonce := mintScope("job-1", "wrong-tree", "pv-1")
	err = cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassCritical, TreeHash: "tree-real", PolicyVersion: "pv-1",
		ApprovalRequestID: reqID, ApprovalNonce: nonce,
	})
	if denial, ok := err.(*ErrorCompletionDenied); !ok || !denial.ExpiredApproval {
		t.Fatalf("Transition with a wrong-tree token = %v, want ExpiredApproval", err)
	}

	// The matching token succeeds.
	reqID2, nonce2 := mintScope("job-1", "tree-real", "pv-1")
	err = cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassCritical, TreeHash: "tree-real", PolicyVersion: "pv-1",
		ApprovalRequestID: reqID2, ApprovalNonce: nonce2,
	})
	if err != nil {
		t.Fatalf("Transition with a matching approval token: %v", err)
	}

	// The SAME token cannot be redeemed twice (single-use).
	job.State = JobStateReviewing
	err = cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassCritical, TreeHash: "tree-real", PolicyVersion: "pv-1",
		ApprovalRequestID: reqID2, ApprovalNonce: nonce2,
	})
	if denial, ok := err.(*ErrorCompletionDenied); !ok || !denial.ExpiredApproval {
		t.Fatalf("Transition redeeming an already-consumed token = %v, want ExpiredApproval", err)
	}
}
