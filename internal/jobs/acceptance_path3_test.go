package jobs_test

// Purpose: P1-E29-W6-S60-T4 Path 3 -- contending-lease admission,
//
//	against the REAL Scheduler.Advance/admitNodes (scheduler_admit.go)
//	and the REAL LeaseManager.Acquire/Release, never a synthetic
//	re-implementation of the ordering rule.
//
// CONTRACT NOTE (quoted in the journal): there is no `queued` JobState
// or LeaseState anywhere in this package (model.go/state.go's closed
// vocabularies). A contending node simply stays un-admitted at
// JobStatePending -- collectAdmissible (scheduler_admit.go) excludes it
// from LeasesToAcquire rather than moving it to a distinct state. This
// test asserts non-admission, matching the real tree, not a `queued`
// state the contract's prose names but the tree does not have.
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/jobs"
)

const acceptanceRepo = "acceptance-repo"

func allowAll(_ context.Context, _ governor.AdmissionRequest) (governor.Permit, error) {
	return governor.Permit{}, nil
}

// applyLeaseIntent drives the REAL LeaseManager for one admitted intent
// and, on grant, moves the job pending->leased->running through the
// REAL public Store path -- the coordinator-loop step Advance itself
// never performs (scheduler.go: "Advance ... makes no DB calls").
func applyLeaseIntent(ctx context.Context, t *testing.T, rig *acceptanceRig, jobID, scopeGlob string) jobs.ResourceLease {
	t.Helper()
	res, err := rig.leases.Acquire(ctx, acceptanceRepo, scopeGlob, jobID)
	if err != nil || !res.Granted {
		t.Fatalf("Acquire(%s, %s) = %+v, err=%v, want Granted", jobID, scopeGlob, res, err)
	}
	if err := rig.store.PutTransition(ctx, jobID, jobs.JobStateLeased, 1); err != nil {
		t.Fatalf("PutTransition(%s)->leased: %v", jobID, err)
	}
	if err := rig.store.PutTransition(ctx, jobID, jobs.JobStateRunning, 2); err != nil {
		t.Fatalf("PutTransition(%s)->running: %v", jobID, err)
	}
	return res.Lease
}

func seedPendingJob(t *testing.T, rig *acceptanceRig, id, scope string, priority int) {
	t.Helper()
	err := rig.store.PutJob(rig.ctx, jobs.Job{
		ID: id, State: jobs.JobStatePending, MutableScope: scope, RiskClass: string(jobs.RiskClassLow),
		MinTaskClass: "code", Priority: priority,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestAcceptancePath3ContendingLease(t *testing.T) {
	rig := newAcceptanceRig(t)
	ctx := rig.ctx
	sched := jobs.NewScheduler(rig.clock)

	const holder = "job-docs-holder"
	seedPendingJob(t, rig, holder, "docs/**", 1)
	holderLease := applyLeaseIntent(ctx, t, rig, holder, "docs/**")

	// A contending node (docs/developer/** intersects docs/**) must
	// never be admitted while the holder's lease is active -- the
	// scheduler's own admissibility rule, exercised against the REAL
	// lease row just acquired above (ListLeases, not a hand-built slice).
	const contenderB, contenderC = "job-docs-dev-b", "job-docs-dev-c"
	seedPendingJob(t, rig, contenderB, "docs/developer/**", 5)
	seedPendingJob(t, rig, contenderC, "docs/developer/**", 1)
	contendDag := jobs.ExecutionDag{Nodes: []jobs.DagNode{
		{ID: contenderB, MutableScope: []string{"docs/developer/**"}, Priority: 5},
		{ID: contenderC, MutableScope: []string{"docs/developer/**"}, Priority: 1},
	}}
	active := activeLeasesForRepo(t, rig)
	states := map[string]jobs.JobState{contenderB: jobs.JobStatePending, contenderC: jobs.JobStatePending}
	delta := sched.Advance(ctx, contendDag, jobs.LeaseAcquired{JobID: holder, LeaseID: holderLease.ScopeGlob}, states, active, allowAll)
	if len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("LeasesToAcquire = %#v while holder's lease is active, want none admitted (not queued, not leased, not an error)", delta.LeasesToAcquire)
	}

	// Explicit release (HOW step 3: "Release the first lease explicitly
	// via AC/S-59.T2 release").
	if err := rig.leases.Release(ctx, acceptanceRepo, "docs/**", holderLease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Re-Advance after release, over the REAL (now-empty) lease view:
	// both contenders are admitted in ONE batch, in the S-59.T5
	// priority-desc/id-asc order -- not FIFO (contenderC was seeded
	// first but carries lower priority).
	afterRelease := activeLeasesForRepo(t, rig)
	delta2 := sched.Advance(ctx, contendDag, jobs.LeaseAcquired{JobID: holder, LeaseID: "released"}, states, afterRelease, allowAll)
	if len(delta2.LeasesToAcquire) != 2 {
		t.Fatalf("LeasesToAcquire after release = %#v, want both contenders admitted", delta2.LeasesToAcquire)
	}
	if delta2.LeasesToAcquire[0].JobID != contenderB || delta2.LeasesToAcquire[1].JobID != contenderC {
		t.Fatalf("admission order = %s,%s, want %s before %s (priority desc, then id asc)",
			delta2.LeasesToAcquire[0].JobID, delta2.LeasesToAcquire[1].JobID, contenderB, contenderC)
	}

	// Drive the winner (contenderB, the higher-priority admission)
	// through a REAL Acquire, proving it actually reaches `running`.
	_ = applyLeaseIntent(ctx, t, rig, contenderB, "docs/developer/**")
	winner, ok, err := rig.store.GetJob(ctx, contenderB)
	if err != nil || !ok || winner.State != jobs.JobStateRunning {
		t.Fatalf("winner state = %+v, ok=%v, err=%v, want running", winner, ok, err)
	}
}

// activeLeasesForRepo reads the REAL held/renewing lease rows back
// through Store.ListLeases -- never a hand-built []ResourceLease -- so
// the scheduler's contention check runs against what Acquire/Release
// actually persisted.
func activeLeasesForRepo(t *testing.T, rig *acceptanceRig) []jobs.ResourceLease {
	t.Helper()
	page, _, err := rig.store.ListLeases(rig.ctx, jobs.LeaseFilter{})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	return page
}
