package jobs

// Purpose: HOW 2c's admissibility rule, deterministic priority/id
//
//	ordering, contending-lease exclusion (including expired_unconfirmed
//	per R-21.139), the fenced-epoch carry-through, and the
//	no-per-node-permit rule (P1-TOP-04c: admitNodes never calls the
//	governor).
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
	if calls != 0 {
		t.Fatalf("governor calls = %d, want 0 (no per-node permit)", calls)
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

// countingGovernor counts calls and refuses every one, so any call it
// sees would also show up as a lost admission.
func countingGovernor(calls *int) GovernorFn {
	return func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
		*calls++
		return governor.Permit{}, governor.ErrQueueFull
	}
}

func TestSchedulerAdmitsNoPerNodePermit(t *testing.T) {
	dag := ExecutionDag{Nodes: []DagNode{
		node("a", 3, "a/**"),
		node("b", 2, "b/**"),
		node("c", 1, "c/**"),
		{ID: "blocked", Deps: []string{"c"}, MutableScope: []string{"d/**"}},
		node("resumed", 0, "e/**"),
	}}
	states := map[string]JobState{"resumed": JobStateLeased}
	var calls int
	delta := NewScheduler(nil).Advance(controllerCtx(), dag, LeaseAcquired{}, states, nil, countingGovernor(&calls))
	if calls != 0 {
		t.Fatalf("governor calls = %d, want 0: admitNodes takes no per-node permit", calls)
	}
	if len(delta.Errors) != 0 {
		t.Fatalf("Errors = %v, want none", delta.Errors)
	}
	got := make([]string, 0, len(delta.LeasesToAcquire))
	for _, l := range delta.LeasesToAcquire {
		got = append(got, l.JobID)
	}
	want := []string{"a", "b", "c", "resumed"}
	if len(got) != len(want) {
		t.Fatalf("admitted = %v, want %v (blocked waits on its dep)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("admitted = %v, want %v", got, want)
		}
	}
	wantTr := StateTransition{JobID: "resumed", From: JobStateLeased, To: JobStateRunning}
	if len(delta.JobsToAdvance) != 1 || delta.JobsToAdvance[0] != wantTr {
		t.Fatalf("JobsToAdvance = %#v, want only %#v", delta.JobsToAdvance, wantTr)
	}
}
