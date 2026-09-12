package jobs

// Purpose: R-21.169's controller-singleton guard as Advance/Resume apply
//
//	it: Guard's pass/refuse shape, and the two-daemon test asserting a
//	node daemon's Advance never schedules and a node daemon's would-be
//	lease.acquire (via LeaseManager's OWN pre-existing isController gate,
//	S-59.T2) grants no second lease for an intersecting scope.
//
// CONTRACT NOTE (quoted in the journal): this file's two-daemon test
// models each "daemon" as its own context.Context role (nodes.WithRole)
// sharing ONE real Store/db, per internal/nodes' own role-resolution
// design (Role is resolved once per connection/request, not once per
// process) -- there is no second real OS process to spawn here because
// R-21.169's guard is a per-call authorization check over a shared
// store, not a second physical lock; scheduler_lock_test.go's own
// "TestSchedulerAdvisoryLockExclusion" (a DIFFERENT scheduler, the
// events/scheduler cron one) establishes the identical "two instances
// sharing one store" pattern as this tree's precedent for testing
// exclusivity, rather than a literal two-OS-process test.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestGuard_ControllerPasses(t *testing.T) {
	if err := Guard(controllerCtx(), "job.advance"); err != nil {
		t.Fatalf("Guard(controller) = %v, want nil", err)
	}
}

func TestGuard_NodeRefuses(t *testing.T) {
	err := Guard(nodeCtx(), "job.advance")
	if err == nil {
		t.Fatal("Guard(node) = nil, want ErrNotController-shaped error")
	}
}

func TestGuard_UnannotatedContextRefusesFailClosed(t *testing.T) {
	// No nodes.WithRole at all: RoleFromContext defaults to RoleNode (the
	// more restrictive role) per its own doc comment -- an un-annotated
	// caller must never accidentally schedule as if it were the
	// controller.
	err := Guard(context.Background(), "job.advance")
	if err == nil {
		t.Fatal("Guard(no role in context) = nil, want fail-closed refusal")
	}
}

// TestTwoDaemons_OnlyControllerSchedulesAndGrantsNoSecondLease is the
// AC/S-59.T5 two-daemon acceptance test: a node-role Advance never
// schedules (Guard refuses before admission runs at all), and
// LeaseManager's own S-59.T2 isController gate grants no second lease
// for the same scope from a node-role caller, over ONE shared store.
func TestControllerSingleton_TwoDaemonsOnlyControllerSchedules(t *testing.T) {
	store := newTestStore(t)
	clock := runtime.NewFixedClock(time.Unix(1000, 0))

	// The "controller daemon": its LeaseManager reports isController=true.
	controllerLM := NewLeaseManager(store, clock, func() bool { return true }, DefaultLeaseDefaults(), nil)
	// The "node daemon": shares the SAME store, but isController=false.
	nodeLM := NewLeaseManager(store, clock, func() bool { return false }, DefaultLeaseDefaults(), nil)

	sched := NewScheduler(clock)
	dag := ExecutionDag{Nodes: []DagNode{node("j1", 1, "a/**")}}

	// The node daemon's Advance refuses outright -- it never even reaches
	// admission, let alone the governor.
	nodeDelta := sched.Advance(nodeCtx(), dag, LeaseAcquired{}, nil, nil, neverCalledGovernor(t))
	if len(nodeDelta.Errors) != 1 {
		t.Fatalf("node daemon Advance: Errors = %v, want exactly one refusal", nodeDelta.Errors)
	}

	// The controller daemon acquires the lease for real.
	granted, err := controllerLM.Acquire(context.Background(), "repo-1", "a/**", "job-1")
	if err != nil || !granted.Granted {
		t.Fatalf("controller Acquire: got %+v, err %v, want granted", granted, err)
	}

	// The node daemon's own Acquire attempt (were it ever reachable) is
	// refused by LeaseManager's own S-59.T2 gate -- no second lease is
	// ever granted for the intersecting scope, from either mechanism.
	_, err = nodeLM.Acquire(context.Background(), "repo-1", "a/**", "job-2")
	if err != ErrNotController {
		t.Fatalf("node daemon Acquire error = %v, want ErrNotController", err)
	}

	// The scope is still held by job-1 alone: a second controller-side
	// Acquire for the same scope is CONTENDED, proving no second lease
	// exists.
	second, err := controllerLM.Acquire(context.Background(), "repo-1", "a/**", "job-2")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if second.Granted {
		t.Fatal("second Acquire granted a second lease for an intersecting scope")
	}
}
