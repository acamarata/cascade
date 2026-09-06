package scheduler

// Purpose: the scheduler's policy-routing gate. The allow/ask/deny
//   behaviour is transcribed from the gate's rule — a scheduled job fires
//   on an allow and on nothing else, and a scheduler with no gate fires
//   nothing — and asserted against those literals.
// Constraints: FrozenClock only; no sleeps.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
)

// stubGate returns one fixed verdict and records the actions it saw.
type stubGate struct {
	verdict policy.Verdict
	err     error
	seen    []routing.Action
}

func (g *stubGate) RouteAction(_ context.Context, action routing.Action) (policy.Verdict, policy.Trace, error) {
	g.seen = append(g.seen, action)
	return g.verdict, policy.Trace{}, g.err
}

// gateSubjectForTest is the principal scheduled dispatches evaluate as.
func gateSubjectForTest() policy.Subject {
	return policy.Subject{Kind: policy.SubjectAgent, ID: "scheduler"}
}

// installGate wires gate onto sched, failing the test if it is refused.
func installGate(t *testing.T, sched *Scheduler, gate ActionGate) {
	t.Helper()
	if err := sched.SetActionGate(gate, gateSubjectForTest(), "scheduler.dispatch"); err != nil {
		t.Fatalf("SetActionGate: %v", err)
	}
}

// activateDueJob registers a counting runnable, schedules a job due one
// minute from the epoch, activates, and advances past it.
func activateDueJob(t *testing.T, sched *Scheduler, clock interface{ Advance(time.Duration) time.Time }, n *int) {
	t.Helper()
	ctx := context.Background()
	if err := sched.RegisterRunnable("owner-a", countingRunnable(n)); err != nil {
		t.Fatalf("RegisterRunnable: %v", err)
	}
	if err := sched.ScheduleJob(ctx, "job-1", "@every 1m", "owner-a"); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if _, err := sched.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close(context.Background()) })
	clock.Advance(2 * time.Minute)
}

// TestSchedulerRouteDispatchAllow proves an allowed dispatch runs and was
// routed with the job's own identity.
func TestSchedulerRouteDispatchAllow(t *testing.T) {
	sched, clock, _, _ := newTestScheduler(t, "owner-a")
	gate := &stubGate{verdict: policy.VerdictAllow}
	installGate(t, sched, gate)
	runs := 0
	activateDueJob(t, sched, clock, &runs)

	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want the allowed job to have fired once", runs)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("Tick errors = %v, want none", report.Errors)
	}
	if len(gate.seen) != 1 {
		t.Fatalf("gate saw %d actions, want 1", len(gate.seen))
	}
	got := gate.seen[0]
	if got.Origin != routing.OriginScheduler || got.Ref != "job-1" ||
		got.Command != ScheduledActionText("owner-a") {
		t.Fatalf("routed action = %+v, want the job's origin, ref and action text", got)
	}
}

// TestSchedulerRouteDispatchDeny proves a denied dispatch never enters the
// Runnable and is recorded as a failure.
func TestSchedulerRouteDispatchDeny(t *testing.T) {
	sched, clock, _, _ := newTestScheduler(t, "owner-a")
	installGate(t, sched, &stubGate{verdict: policy.VerdictDeny})
	runs := 0
	activateDueJob(t, sched, clock, &runs)

	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want a denied job never to run", runs)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Tick errors = %v, want the refusal recorded", report.Errors)
	}
}

// TestSchedulerRouteDispatchAsk proves an ask suspends the dispatch: a
// scheduled job runs on an allow and on nothing else.
func TestSchedulerRouteDispatchAsk(t *testing.T) {
	sched, clock, _, _ := newTestScheduler(t, "owner-a")
	installGate(t, sched, &stubGate{verdict: policy.VerdictAsk})
	runs := 0
	activateDueJob(t, sched, clock, &runs)

	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want an asked job not to run", runs)
	}
}

// TestSchedulerRouteDispatchGateError proves a routing call that fails
// refuses the dispatch: the unavailable case never becomes a fire.
func TestSchedulerRouteDispatchGateError(t *testing.T) {
	sched, clock, _, _ := newTestScheduler(t, "owner-a")
	installGate(t, sched, &stubGate{verdict: policy.Verdict(0), err: errors.New("engine unavailable")})
	runs := 0
	activateDueJob(t, sched, clock, &runs)

	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want a failed routing call to suppress the dispatch", runs)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Tick errors = %v, want the failure recorded", report.Errors)
	}
}

// TestSchedulerRouteDispatchUnwiredFiresNothing proves the fail-closed
// default: a scheduler nobody wired a gate onto dispatches nothing.
func TestSchedulerRouteDispatchUnwiredFiresNothing(t *testing.T) {
	sched, clock, _, _ := newTestSchedulerUngated(t, "owner-a")
	runs := 0
	activateDueJob(t, sched, clock, &runs)

	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want an unrouted scheduler to fire nothing", runs)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("Tick errors = %v, want the unrouted refusal recorded", report.Errors)
	}
}

// TestSetActionGateRefusesIncompleteWiring proves none of the three
// routing collaborators is optional.
func TestSetActionGateRefusesIncompleteWiring(t *testing.T) {
	sched, _, _, _ := newTestSchedulerUngated(t, "owner-a")
	if err := sched.SetActionGate(nil, gateSubjectForTest(), "scheduler.dispatch"); err == nil {
		t.Fatal("a nil gate was accepted")
	}
	if err := sched.SetActionGate(&stubGate{}, policy.Subject{}, "scheduler.dispatch"); err == nil {
		t.Fatal("a subject naming nobody was accepted")
	}
	if err := sched.SetActionGate(&stubGate{}, gateSubjectForTest(), ""); err == nil {
		t.Fatal("an empty capability was accepted")
	}
}
