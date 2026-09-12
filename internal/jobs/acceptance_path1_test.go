package jobs_test

// Purpose: P1-E29-W6-S60-T4 Path 1 -- the happy-path plan -> lease ->
//
//	worktree -> evidence -> accepted lifecycle, asserting STORE state at
//	every stage (never only the emitted event), per the ticket's own
//	"a defect found today was exactly this: the resume path emitted a
//	transition event that nothing applied" instruction.
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
)

func TestAcceptancePath1HappyPath(t *testing.T) {
	rig := newAcceptanceRig(t)
	ctx := rig.ctx

	node := planFixtureNode(t, rig)
	acquired := leaseAndWorktree(t, rig, node)

	if err := rig.store.PutTransition(ctx, node.ID, jobs.JobStateRunning, 2); err != nil {
		t.Fatalf("PutTransition ->running: %v", err)
	}
	if err := rig.store.PutExecution(ctx, jobs.Execution{ID: "exec-" + node.ID, JobID: node.ID, Attempt: 1, State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}

	appendPass(t, rig, node.ID, acquired, jobs.EvidenceLint, "idem-lint-1")
	appendPass(t, rig, node.ID, acquired, jobs.EvidenceTests, "idem-tests-1")

	runCompletionToAccepted(t, rig, node.ID, acquired)

	final, ok, err := rig.store.GetJob(ctx, node.ID)
	if err != nil || !ok || final.State != jobs.JobStateAccepted {
		t.Fatalf("final job state = %+v, ok=%v, err=%v, want accepted", final, ok, err)
	}

	for _, kind := range []jobs.EvidenceKind{jobs.EvidenceLint, jobs.EvidenceTests, jobs.EvidenceReview} {
		rec, err := rig.ledger.Query(ctx, node.ID, kind)
		if err != nil || rec.Outcome != jobs.OutcomePass {
			t.Fatalf("Query(%s) = %+v, err=%v, want outcome=pass", kind, rec, err)
		}
	}
}

// planFixtureNode compiles the PEWS fixture and plans it through the
// REAL AC/S-59.T4 planner, asserting the classifier's Low verdict and
// its verbatim gate set, then seeds the resulting node's store row.
func planFixtureNode(t *testing.T, rig *acceptanceRig) jobs.DagNode {
	t.Helper()
	contract := loadFixtureContract(t)
	planInput, _, err := jobs.CompileTicket(contract, nil)
	if err != nil {
		t.Fatalf("CompileTicket: %v", err)
	}
	dag, err := jobs.NewPlanner(nil).Plan(rig.ctx, planInput, sessionScope(rig.repoRoot))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(dag.Nodes) != 1 {
		t.Fatalf("dag.Nodes = %d, want 1 (Planner.Plan yields one node per ticket; see this file's CONTRACT NOTE)", len(dag.Nodes))
	}
	node := dag.Nodes[0]
	if node.RiskClass != jobs.RiskClassLow {
		t.Fatalf("node.RiskClass = %q, want %q", node.RiskClass, jobs.RiskClassLow)
	}
	gates, err := jobs.GateSetForRiskClass(node.RiskClass)
	if err != nil || len(gates) != 3 || gates[0] != jobs.GateFormat || gates[1] != jobs.GateStatic || gates[2] != jobs.GateTargetedVerification {
		t.Fatalf("GateSetForRiskClass(Low) = %v, err=%v, want verbatim {format,static,targeted_verification}", gates, err)
	}
	seedJob(t, rig, node)
	return node
}

// leaseAndWorktree acquires the REAL lease and creates the REAL git
// worktree for node, asserting both store-observable results, and
// registers the worktree's cleanup.
func leaseAndWorktree(t *testing.T, rig *acceptanceRig, node jobs.DagNode) jobs.ResourceLease {
	t.Helper()
	ctx := rig.ctx
	acquired, err := rig.leases.Acquire(ctx, "acceptance-repo", "docs/**", node.ID)
	if err != nil || !acquired.Granted {
		t.Fatalf("Acquire = %+v, err=%v, want Granted", acquired, err)
	}
	if acquired.Lease.ScopeGlob == "" || acquired.Lease.IssuedAt == 0 {
		t.Fatalf("lease = %+v, want non-empty scope_glob and non-zero issued_at", acquired.Lease)
	}
	if err := rig.store.PutTransition(ctx, node.ID, jobs.JobStateLeased, 1); err != nil {
		t.Fatalf("PutTransition ->leased: %v", err)
	}
	wt, err := rig.worktree.Create(ctx, acquired.Lease, rig.repoRoot)
	if err != nil {
		t.Fatalf("worktree Create: %v", err)
	}
	if wt.Branch != "job/"+node.ID {
		t.Fatalf("wt.Branch = %q, want job/%s", wt.Branch, node.ID)
	}
	t.Cleanup(func() { _ = rig.worktree.Remove(context.Background(), acquired.Lease) })
	return acquired.Lease
}

// runCompletionToAccepted drives the REAL CompletionPolicy through
// running->verifying->reviewing->accepted, appending the review
// evidence between reviewing's admission and the final transition.
func runCompletionToAccepted(t *testing.T, rig *acceptanceRig, jobID string, lease jobs.ResourceLease) {
	t.Helper()
	ctx := rig.ctx
	job, ok, err := rig.store.GetJob(ctx, jobID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	req := func(target jobs.JobState) jobs.TransitionRequest {
		return jobs.TransitionRequest{
			Job: &job, Target: target, Caller: jobs.PolicyEngineIdentity{EngineID: acceptanceEngineID},
			PlannedRiskClass:   jobs.RiskClassLow,
			ActualFootprint:    jobs.ChangeFootprint{ChangedPaths: []string{"docs/fixture-a.md", "docs/fixture-b.md"}},
			LeaseScopePrefixes: []string{"docs/"},
		}
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateVerifying)); err != nil {
		t.Fatalf("Transition ->verifying: %v", err)
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateReviewing)); err != nil {
		t.Fatalf("Transition ->reviewing: %v", err)
	}
	appendPass(t, rig, jobID, lease, jobs.EvidenceReview, "idem-review-1")
	if err := rig.policy.Transition(ctx, req(jobs.JobStateAccepted)); err != nil {
		t.Fatalf("Transition ->accepted: %v", err)
	}
}

// seedJob writes the DagNode's initial pending row -- the DAG planner
// (planner.go) produces a static node with no store row of its own; a
// real coordinator (out of this ticket's files_scope) would do this
// same PutJob at admission time.
func seedJob(t *testing.T, rig *acceptanceRig, node jobs.DagNode) {
	t.Helper()
	err := rig.store.PutJob(rig.ctx, jobs.Job{
		ID: node.ID, State: jobs.JobStatePending, CreatedAt: 0, UpdatedAt: 0,
		MutableScope: "docs/**", RiskClass: string(node.RiskClass), MinTaskClass: string(node.MinTaskClass),
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	})
	if err != nil {
		t.Fatalf("seed PutJob: %v", err)
	}
}

// appendPass appends one real, controller-authorized, passing evidence
// row for jobID -- the REAL EvidenceLedger.Append path (evidence.go),
// never a hand-authored row.
func appendPass(t *testing.T, rig *acceptanceRig, jobID string, lease jobs.ResourceLease, kind jobs.EvidenceKind, idem string) {
	t.Helper()
	rec := jobs.EvidenceRecord{
		JobID: jobID, Kind: kind, ProducerCapability: jobs.ProducerControllerRun,
		AttemptID: "exec-" + jobID, AttestorIdentity: "daemon:acceptance",
		Outcome: jobs.OutcomePass, IdempotencyKey: idem,
	}
	auth := jobs.AppendAuthorization{
		ExecutionID: "exec-" + jobID, LeaseRepoID: lease.RepoID,
		LeaseScopeGlob: lease.ScopeGlob, LeaseEpoch: lease.Epoch,
	}
	if _, err := rig.ledger.Append(rig.ctx, rec, auth); err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
}
