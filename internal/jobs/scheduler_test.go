package jobs

// Purpose: Scheduler.Advance's dispatch behavior: idempotent cancel over
//
//	all four terminal states plus cancelling, TerminationConfirmed's
//	sole path to `cancelled`, LeaseExpired's attention-item/no-mutation
//	contract, and the unknown-event fail-closed path.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

// controllerCtx returns a context Advance/Resume accept: RoleController,
// via the real internal/nodes seam (no second guard mechanism).
func controllerCtx() context.Context {
	return nodes.WithRole(context.Background(), nodes.RoleController)
}

// nodeCtx returns a context Advance/Resume refuse: RoleNode.
func nodeCtx() context.Context {
	return nodes.WithRole(context.Background(), nodes.RoleNode)
}

func neverCalledGovernor(t *testing.T) GovernorFn {
	return func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
		t.Fatal("governorFn called but no node should have been admissible")
		return governor.Permit{}, nil
	}
}

func allowAllGovernor(calls *int) GovernorFn {
	return func(context.Context, governor.AdmissionRequest) (governor.Permit, error) {
		*calls++
		return governor.Permit{}, nil
	}
}

func TestAdvance_RefusesOnNonController(t *testing.T) {
	sched := NewScheduler(nil)
	delta := sched.Advance(nodeCtx(), ExecutionDag{}, CancelRequested{JobID: "j1"}, nil, nil, neverCalledGovernor(t))
	if len(delta.Errors) != 1 {
		t.Fatalf("Advance on a node daemon: Errors = %v, want exactly one ErrNotController-shaped error", delta.Errors)
	}
}

func TestCancelling_IdempotentOnTerminalStates(t *testing.T) {
	sched := NewScheduler(nil)
	terminal := []JobState{JobStateAccepted, JobStateRejected, JobStateCancelled, JobStateFailed, JobStateCancelling}
	for _, st := range terminal {
		t.Run(string(st), func(t *testing.T) {
			states := map[string]JobState{"j1": st}
			delta := sched.Advance(controllerCtx(), ExecutionDag{}, CancelRequested{JobID: "j1"}, states, nil, neverCalledGovernor(t))
			if len(delta.JobsToAdvance) != 0 || len(delta.OutboxIntents) != 0 || len(delta.Errors) != 0 {
				t.Fatalf("cancel on %s: delta = %#v, want empty (idempotent)", st, delta)
			}
		})
	}
}

func TestAdvance_CancelOnNonTerminalEmitsOneCancellingTransition(t *testing.T) {
	sched := NewScheduler(nil)
	nonTerminal := []JobState{JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying, JobStateReviewing}
	for _, st := range nonTerminal {
		t.Run(string(st), func(t *testing.T) {
			states := map[string]JobState{"j1": st}
			delta := sched.Advance(controllerCtx(), ExecutionDag{}, CancelRequested{JobID: "j1"}, states, nil, neverCalledGovernor(t))
			if len(delta.JobsToAdvance) != 1 || delta.JobsToAdvance[0].To != JobStateCancelling {
				t.Fatalf("cancel on %s: JobsToAdvance = %#v, want exactly one ->cancelling", st, delta.JobsToAdvance)
			}
			if len(delta.OutboxIntents) != 1 {
				t.Fatalf("cancel on %s: OutboxIntents = %#v, want exactly one intent", st, delta.OutboxIntents)
			}
		})
	}
}

func TestAdvance_TerminationConfirmedOnlyFromCancelling(t *testing.T) {
	sched := NewScheduler(nil)

	states := map[string]JobState{"j1": JobStateCancelling}
	delta := sched.Advance(controllerCtx(), ExecutionDag{}, TerminationConfirmed{JobID: "j1"}, states, nil, neverCalledGovernor(t))
	if len(delta.JobsToAdvance) != 1 || delta.JobsToAdvance[0].To != JobStateCancelled {
		t.Fatalf("TerminationConfirmed from cancelling: JobsToAdvance = %#v, want exactly one ->cancelled", delta.JobsToAdvance)
	}

	statesRunning := map[string]JobState{"j1": JobStateRunning}
	delta2 := sched.Advance(controllerCtx(), ExecutionDag{}, TerminationConfirmed{JobID: "j1"}, statesRunning, nil, neverCalledGovernor(t))
	if len(delta2.JobsToAdvance) != 0 {
		t.Fatalf("TerminationConfirmed from running (never requested cancel): JobsToAdvance = %#v, want empty", delta2.JobsToAdvance)
	}
}

func TestAdvance_LeaseExpiredEmitsAttentionNoMutation(t *testing.T) {
	sched := NewScheduler(nil)
	delta := sched.Advance(controllerCtx(), ExecutionDag{}, LeaseExpired{JobID: "j1", RepoID: "r1", ScopeGlob: "a/**"}, nil, nil, neverCalledGovernor(t))
	if len(delta.JobsToAdvance) != 0 {
		t.Fatalf("LeaseExpired: JobsToAdvance = %#v, want empty (never mutate state)", delta.JobsToAdvance)
	}
	if len(delta.EventsToEmit) != 1 {
		t.Fatalf("LeaseExpired: EventsToEmit = %#v, want exactly one attention item", delta.EventsToEmit)
	}
	if _, ok := delta.EventsToEmit[0].(AttentionRaised); !ok {
		t.Fatalf("LeaseExpired: EventsToEmit[0] = %#v, want AttentionRaised", delta.EventsToEmit[0])
	}
}

func TestAdvance_UnknownEventFailsClosedNonPanicking(t *testing.T) {
	sched := NewScheduler(nil)
	delta := sched.Advance(controllerCtx(), ExecutionDag{}, unknownTestEvent{}, nil, nil, neverCalledGovernor(t))
	if len(delta.Errors) != 1 {
		t.Fatalf("unknown event: Errors = %v, want exactly one error", delta.Errors)
	}
}

// unknownTestEvent implements Event but is not one of the six real
// variants -- proves Advance's default case, not just DecodeEvent's.
type unknownTestEvent struct{}

func (unknownTestEvent) eventKind() string { return "unknown_test_event" }

func TestResume_NilStoreRefused(t *testing.T) {
	sched := NewScheduler(runtime.NewFixedClock(time.Unix(0, 0)))
	if _, err := sched.Resume(controllerCtx(), nil, nil, 0, nil, nil); err == nil {
		t.Fatal("Resume(nil store) = nil, want an error")
	}
}
