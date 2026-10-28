package jobs_test

// Purpose: a job reaching accepted through the completion policy's commit
//
//	path releases every lease it holds, through the release-on-terminal hook
//	jobs.WireLeaseRelease installs on the store (AUD-043 item 3).
//
// SPORT: jobs/lease-model (ADD, P1-CAP-10).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
)

// TestLeaseAutoReleasedOnCommit drives the acceptance rig's real policy to
// accepted and reads the lease row back as released.
func TestLeaseAutoReleasedOnCommit(t *testing.T) {
	rig := newAcceptanceRig(t)
	ctx := rig.ctx
	node := planFixtureNode(t, rig)
	lease := leaseAndWorktree(t, rig, node)
	if err := rig.store.PutTransition(ctx, node.ID, jobs.JobStateRunning, 2); err != nil {
		t.Fatalf("PutTransition ->running: %v", err)
	}
	if err := rig.store.PutExecution(ctx, jobs.Execution{ID: "exec-" + node.ID, JobID: node.ID, Attempt: 1, State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	appendPass(t, rig, node.ID, lease, jobs.EvidenceLint, "idem-lint-commit")
	appendPass(t, rig, node.ID, lease, jobs.EvidenceTests, "idem-tests-commit")
	runCompletionToAccepted(t, rig, node.ID, lease)
	stored, ok, err := rig.store.GetLease(ctx, lease.RepoID, lease.ScopeGlob)
	if err != nil || !ok || stored.State != jobs.LeaseReleased {
		t.Fatalf("lease after accepted = %+v, ok=%v, err=%v; want released by the commit path", stored, ok, err)
	}
}

// TestTerminalTransitionOnNodeRoleIsNotAFailure: on a node daemon the
// release hook has no authority (ErrNotController); the committed terminal
// transition still reports success and the lease is left to the controller.
func TestTerminalTransitionOnNodeRoleIsNotAFailure(t *testing.T) {
	rig := newAcceptanceRig(t)
	ctx := context.Background()
	node := planFixtureNode(t, rig)
	lease := leaseAndWorktree(t, rig, node)
	jobs.WireLeaseRelease(rig.store, jobs.NewLeaseManager(rig.store, rig.clock, func() bool { return false }, jobs.DefaultLeaseDefaults(), nil))
	if err := rig.store.PutTransition(ctx, node.ID, jobs.JobStateFailed, 3); err != nil {
		t.Fatalf("PutTransition ->failed on a node daemon = %v, want success", err)
	}
	if l, _, _ := rig.store.GetLease(ctx, lease.RepoID, lease.ScopeGlob); l.State != jobs.LeaseHeld {
		t.Fatalf("lease = %q, want held (no authority to release on a node)", l.State)
	}
}
