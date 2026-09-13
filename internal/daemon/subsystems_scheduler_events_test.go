package daemon

// Purpose: closes two branches subsystems_scheduler_test.go's existing
//
//	suite does not reach: dispatchSchedulerEvent's decode-error
//	propagation (a malformed bus payload must abort the dispatch, not be
//	swallowed) and runSchedulerConsumer's closed-Events-channel exit
//	route (distinct from the ctx.Done() route
//	TestRunSchedulerConsumer_ExitsOnContextCancellation already proves).
//	Split to its own file rather than appended to
//	subsystems_scheduler_test.go, which is already near the 300-line cap.
//
// SPORT: internal/daemon (TEST, merge-fix for P1-E29-W6-S59-T5).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestDispatchSchedulerEvent_DecodeErrorPropagates proves a malformed bus
// payload's decode failure surfaces as dispatchSchedulerEvent's own
// return value rather than being silently dropped -- the one error route
// through decodeSchedulerEvent that TestDispatchSchedulerEvent_
// NodeRoleRefusesWithoutError does not exercise (it uses a well-formed
// payload to isolate the Guard-refusal branch instead).
func TestDispatchSchedulerEvent_DecodeErrorPropagates(t *testing.T) {
	sched := jobs.NewScheduler(runtime.NewSystemClock())
	ctrlCtx := nodes.WithRole(context.Background(), nodes.RoleController)
	ev := events.Event{Kind: jobs.EventLeaseAcquired, Payload: []byte("not json")}

	err := dispatchSchedulerEvent(ctrlCtx, sched, ev)
	if err == nil {
		t.Fatal("dispatchSchedulerEvent() with a malformed payload = nil error, want the decode failure to propagate")
	}
}

// TestRunSchedulerConsumer_ExitsOnClosedEventsChannel proves the consumer
// loop's "!ok" branch on sub.Events -- distinct from the ctx.Done() exit
// route -- returns cleanly when the subscription itself closes (e.g. an
// operator unsubscribes) while ctx is still live.
func TestRunSchedulerConsumer_ExitsOnClosedEventsChannel(t *testing.T) {
	bus := newSchedulerTestBus()
	ctx := context.Background()
	sub, err := bus.Subscribe(ctx, jobs.LeaseEventNamespace, "sched-test-closed-channel", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sched := jobs.NewScheduler(runtime.NewSystemClock())

	done := make(chan error, 1)
	go func() { done <- runSchedulerConsumer(nodes.WithRole(ctx, nodes.RoleController), sched, sub) }()

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSchedulerConsumer() = %v, want nil on a closed Events channel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSchedulerConsumer did not exit within 2s of Unsubscribe")
	}
}
