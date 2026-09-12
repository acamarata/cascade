package jobs

// Purpose: HOW 2c's admissibility rule, deterministic priority/id
//
//	ordering, contending-lease exclusion (including expired_unconfirmed
//	per R-21.139), the fenced-epoch carry-through, and the
//	Admit-once-per-admissible-node governor contract.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
)

func node(id string, priority int, scope ...string) DagNode {
	return DagNode{ID: id, MutableScope: scope, Priority: priority}
}

func TestAdmit_TwoNonContendingNodesBothAdmittedInPriorityOrder(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{
		node("low", 1, "a/**"),
		node("high", 10, "b/**"),
	}}
	var calls int
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{JobID: "x", LeaseID: "l"}, nil, nil, allowAllGovernor(&calls))
	if len(delta.LeasesToAcquire) != 2 {
		t.Fatalf("LeasesToAcquire = %#v, want 2 non-contending nodes admitted", delta.LeasesToAcquire)
	}
	if delta.LeasesToAcquire[0].JobID != "high" || delta.LeasesToAcquire[1].JobID != "low" {
		t.Fatalf("order = %s,%s, want high before low (priority desc)", delta.LeasesToAcquire[0].JobID, delta.LeasesToAcquire[1].JobID)
	}
	if calls != 2 {
		t.Fatalf("governor calls = %d, want exactly one per admissible node (2)", calls)
	}
}

func TestAdmit_DeterministicTiebreakByIDAscending(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{
		node("zulu", 5, "a/**"),
		node("alpha", 5, "b/**"),
	}}
	var calls int
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, nil, nil, allowAllGovernor(&calls))
	if len(delta.LeasesToAcquire) != 2 || delta.LeasesToAcquire[0].JobID != "alpha" || delta.LeasesToAcquire[1].JobID != "zulu" {
		t.Fatalf("order = %#v, want alpha before zulu (same priority, id asc)", delta.LeasesToAcquire)
	}
}

func TestAdmit_ContendingLeaseExcludesNode(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{node("j1", 1, "a/**")}}
	active := []ResourceLease{{RepoID: "r1", ScopeGlob: "a/**", State: LeaseHeld}}
	var calls int
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, nil, active, allowAllGovernor(&calls))
	if len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("LeasesToAcquire = %#v, want empty (contending scope)", delta.LeasesToAcquire)
	}
	if calls != 0 {
		t.Fatalf("governor calls = %d, want 0 (never called for a non-admissible node)", calls)
	}
}

func TestAdmit_ExpiredUnconfirmedLeaseStillCounts(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{node("j1", 1, "a/**")}}
	active := []ResourceLease{{RepoID: "r1", ScopeGlob: "a/**", State: LeaseExpiredUnconfirmed, Epoch: 4}}
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, nil, active, neverCalledGovernor(t))
	if len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("LeasesToAcquire = %#v, want empty: expired_unconfirmed still ACTIVE per R-21.139", delta.LeasesToAcquire)
	}
}

func TestAdmit_LeaseReclaimedFreesScope(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{node("j1", 1, "a/**")}}
	// No active lease passed (the caller's own view already dropped the
	// reclaimed row) -- LeaseReclaimed re-triggers admission the same as
	// LeaseAcquired/JobTransitioned.
	var calls int
	delta := sched.Advance(controllerCtx(), dag, LeaseReclaimed{RepoID: "r1", ScopeGlob: "a/**", NewEpoch: 5}, nil, nil, allowAllGovernor(&calls))
	if len(delta.LeasesToAcquire) != 1 {
		t.Fatalf("LeasesToAcquire = %#v, want the freed node admitted", delta.LeasesToAcquire)
	}
}

func TestAdmit_DepsMustAllBeAccepted(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{{ID: "child", Deps: []string{"parent"}, MutableScope: []string{"a/**"}}}}
	notYet := map[string]JobState{"parent": JobStateRunning}
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, notYet, nil, neverCalledGovernor(t))
	if len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("LeasesToAcquire = %#v, want empty: dep not yet accepted", delta.LeasesToAcquire)
	}

	accepted := map[string]JobState{"parent": JobStateAccepted}
	var calls int
	delta2 := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, accepted, nil, allowAllGovernor(&calls))
	if len(delta2.LeasesToAcquire) != 1 {
		t.Fatalf("LeasesToAcquire = %#v, want the child admitted once its dep is accepted", delta2.LeasesToAcquire)
	}
}

func TestAdmit_QueueFullStopsIteration(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{node("a", 2, "a/**"), node("b", 1, "b/**")}}
	var calls int
	gov := func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
		calls++
		return governor.Permit{}, governor.ErrQueueFull
	}
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, nil, nil, gov)
	if calls != 1 {
		t.Fatalf("governor calls = %d, want exactly 1 (iteration stops on ErrQueueFull)", calls)
	}
	if len(delta.Errors) != 1 || len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("delta = %#v, want one recorded error and no admissions", delta)
	}
}

func TestAdmit_MalformedScopeRecordsErrorAndSkipsNode(t *testing.T) {
	sched := NewScheduler(nil)
	dag := ExecutionDag{Nodes: []DagNode{node("bad", 1, "/absolute/not/allowed")}}
	delta := sched.Advance(controllerCtx(), dag, LeaseAcquired{}, nil, nil, neverCalledGovernor(t))
	if len(delta.Errors) != 1 {
		t.Fatalf("Errors = %v, want exactly one malformed-scope error", delta.Errors)
	}
	if len(delta.LeasesToAcquire) != 0 {
		t.Fatalf("LeasesToAcquire = %#v, want empty", delta.LeasesToAcquire)
	}
}
