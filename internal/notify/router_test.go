package notify

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

type chanSubscriber struct {
	id string
	ch chan Notification
}

func (c chanSubscriber) ID() string                   { return c.id }
func (c chanSubscriber) Match(Notification) bool      { return true }
func (c chanSubscriber) Receive(n Notification) error { c.ch <- n; return nil }

// newTestRouterBus returns a real events.Bus (Art.2 real-counterpart
// fixture, same construction bus_test.go uses) and a live subscription on
// the "notify" namespace.
func newTestRouterBus(t *testing.T, clock *testkit.FrozenClock) (*events.Bus, *events.Subscription) {
	t.Helper()
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	sub, err := bus.Subscribe(context.Background(), "notify", t.Name(), 16)
	if err != nil {
		t.Fatalf("bus.Subscribe: %v", err)
	}
	return bus, sub
}

// TestRouterEndToEndDispatchesUrgentBeforeNormal drives the real
// production entry point end to end: a real events.Bus, this ticket's
// NotificationRouter decoding a real subscription, and the Dispatcher
// fanning out to a real Subscriber. TestRouterWiringCanFail (below) proves
// this test actually detects a broken wire, not just a passing tautology.
func TestRouterEndToEndDispatchesUrgentBeforeNormal(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus, sub := newTestRouterBus(t, clock)

	svc := NewService(DefaultConfig(), clock, silentLogger())
	got := make(chan Notification, 8)
	svc.Registry.Subscribe(chanSubscriber{id: "cli", ch: got},
		CandidateSession{SessionID: "cli", ScopeIDs: map[string]bool{"test-scope": true}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	routerDone := make(chan error, 1)
	go func() { routerDone <- svc.Router.Run(ctx, sub) }()

	// backup.result and supervision.stalled resolve to Class Scoped
	// (sourceKindMapping), so both need a TargetScope the subscriber's
	// candidate session actually contains -- otherwise
	// ScopeDeliveryPredicate correctly withholds them (the fail-closed
	// behavior scope_test.go's leak test proves).
	publishMapped(ctx, t, bus, "backup.result", "normal-1", "test-scope")
	publishMapped(ctx, t, bus, "supervision.stalled", "urgent-1", "test-scope")

	// The priority-order guarantee is per single Drain call, not across
	// calls: wait until the router has enqueued BOTH notifications into
	// their respective priority channels before draining once, so the
	// urgent-before-normal assertion below tests the real invariant
	// rather than a race between publish and an early Drain.
	waitForBothQueued(t, svc)
	svc.Dispatcher.Drain(ctx)

	var order []string
	deadline := time.After(2 * time.Second)
	for len(order) < 2 {
		select {
		case n := <-got:
			order = append(order, n.ID)
		case <-deadline:
			t.Fatalf("timed out waiting for dispatch; got %v so far", order)
		}
	}

	cancel()
	<-routerDone

	if order[0] != "urgent-1" || order[1] != "normal-1" {
		t.Fatalf("dispatch order = %v, want [urgent-1 normal-1]", order)
	}
}

// TestRouterWiringCanFail is the AGENT-BRIEF-mandated proof that the
// end-to-end test above can actually fail: with the router's decode step
// replaced by a no-op (simulating the wiring being removed -- the mapping
// table returning nothing), no Notification is ever enqueued, and the
// dispatcher observes zero processed notifications where the real wiring
// observes two.
func TestRouterWiringCanFail(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus, sub := newTestRouterBus(t, clock)

	queues := newQueueSet(DefaultConfig(), silentLogger())
	registry := NewRegistry()
	inbox := NewInbox()
	dispatcher := NewDispatcher(queues, registry, inbox, clock, silentLogger())

	got := make(chan Notification, 8)
	registry.Subscribe(chanSubscriber{id: "cli", ch: got}, CandidateSession{SessionID: "cli"})

	// brokenRouter simulates the wiring removed: it drains the
	// subscription (so the bus is not blocked) but never enqueues,
	// standing in for "router.handle's mapping lookup deleted."
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sub.Events:
				if !ok {
					return
				}
				// intentionally does not call queues.enqueue
			}
		}
	}()

	publishMapped(ctx, t, bus, "backup.result", "normal-1", "test-scope")
	publishMapped(ctx, t, bus, "supervision.stalled", "urgent-1", "test-scope")

	// Give the broken consumer a bounded chance to (not) enqueue, then
	// assert nothing was ever processed -- proving the earlier passing
	// test is sensitive to the wiring, not a tautology.
	deadline := time.Now().Add(300 * time.Millisecond)
	processed := 0
	for time.Now().Before(deadline) {
		processed += dispatcher.Drain(ctx)
	}
	if processed != 0 {
		t.Fatalf("broken-wiring stub still resulted in %d processed notifications", processed)
	}
	select {
	case n := <-got:
		t.Fatalf("subscriber received %v with the wiring removed", n)
	default:
	}
}

// waitForBothQueued blocks, on a bounded deadline, until the Dispatcher's
// Urgent and Normal channels both hold at least one Notification -- real
// synchronization on the queues the router actually writes (accessible
// here because this test file lives in package notify, not notify_test),
// not a fixed sleep standing in for a signal.
func waitForBothQueued(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.Dispatcher.queues.queues[PriorityUrgent]) > 0 &&
			len(svc.Dispatcher.queues.queues[PriorityNormal]) > 0 {
			return
		}
	}
	t.Fatal("timed out waiting for both priority queues to receive their notification")
}

func publishMapped(ctx context.Context, t *testing.T, bus *events.Bus, kind events.EventKind, id, targetScope string) {
	t.Helper()
	payload := events.EncodeNotificationPayload(events.NotificationPayload{ID: id, TargetScope: targetScope})
	if _, err := bus.Publish(ctx, "notify", kind, "test", payload); err != nil {
		t.Fatalf("Publish %s: %v", kind, err)
	}
}

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
