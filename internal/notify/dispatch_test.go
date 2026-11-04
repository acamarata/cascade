package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestDispatcher(t *testing.T, now time.Time) (*Dispatcher, *queueSet, *Registry, *Inbox) {
	t.Helper()
	cfg := DefaultConfig()
	queues := newQueueSet(cfg, silentLogger())
	registry := NewRegistry()
	inbox := NewInbox()
	clk := fixedClock{now: now}
	d := NewDispatcher(queues, registry, inbox, clk, silentLogger())
	return d, queues, registry, inbox
}

func recordingSubscriber(id string, matches bool) (*stubSubscriber, *[]Notification) {
	var mu sync.Mutex
	var got []Notification
	sub := &stubSubscriber{
		id:      id,
		matches: matches,
		recv: func(n Notification) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, n)
			return nil
		},
	}
	return sub, &got
}

func TestDispatchFanOutTwoMatchingSubscribers(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	subA, gotA := recordingSubscriber("a", true)
	subB, gotB := recordingSubscriber("b", true)
	subC, gotC := recordingSubscriber("c", false) // predicate false: does not match
	registry.Subscribe(subA, session("a"))
	registry.Subscribe(subB, session("b"))
	registry.Subscribe(subC, session("c"))

	queues.enqueue(Notification{ID: "n1", Priority: PriorityNormal, Class: ClassGlobalCritical})
	d.Drain(context.Background())

	if len(*gotA) != 1 || len(*gotB) != 1 {
		t.Fatalf("expected both matching subscribers to receive: a=%d b=%d", len(*gotA), len(*gotB))
	}
	if len(*gotC) != 0 {
		t.Fatalf("subscriber whose Match returns false received the notification")
	}
}

func TestDispatchSubscriberErrorDoesNotBlockOthers(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	failing := stubSubscriber{id: "fail", matches: true, recv: func(Notification) error { return errors.New("boom") }}
	okSub, gotOK := recordingSubscriber("ok", true)
	registry.Subscribe(failing, session("fail"))
	registry.Subscribe(okSub, session("ok"))

	queues.enqueue(Notification{ID: "n1", Priority: PriorityNormal, Class: ClassGlobalCritical})
	d.Drain(context.Background())

	if len(*gotOK) != 1 {
		t.Fatalf("a failing subscriber blocked delivery to a healthy one: got %d", len(*gotOK))
	}
}

func TestDispatchSubscriberErrorRequeuesForNextDrain(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	attempts := 0
	failing := stubSubscriber{id: "fail", matches: true, recv: func(Notification) error {
		attempts++
		if attempts == 1 {
			return errors.New("boom")
		}
		return nil
	}}
	registry.Subscribe(failing, session("fail"))

	queues.enqueue(Notification{ID: "n1", Priority: PriorityNormal, Class: ClassGlobalCritical})
	d.Drain(context.Background()) // first attempt fails, re-queues
	if attempts != 1 {
		t.Fatalf("attempts after first drain = %d, want 1", attempts)
	}
	d.Drain(context.Background()) // second drain retries
	if attempts != 2 {
		t.Fatalf("attempts after second drain = %d, want 2 (re-delivery expected)", attempts)
	}
}

func TestDispatchLedgerPreventsDoubleDispatchSameCycle(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	sub, got := recordingSubscriber("a", true)
	registry.Subscribe(sub, session("a"))

	// Simulate the same notification appearing twice within one drain
	// cycle (e.g. queued at two priorities by a misbehaving producer).
	n := Notification{ID: "dup", Priority: PriorityNormal, Class: ClassGlobalCritical}
	queues.enqueue(n)
	queues.queues[PriorityHigh] <- n // second, same ID, different priority lane

	d.Drain(context.Background())

	if len(*got) != 1 {
		t.Fatalf("subscriber received the same notification %d times in one drain cycle, want 1", len(*got))
	}
}

func TestDispatchWithheldCounted(t *testing.T) {
	d, queues, registry, inbox := newTestDispatcher(t, time.Unix(100, 0))
	sub, got := recordingSubscriber("a", true)
	registry.Subscribe(sub, session("a", "project-1"))

	n := Notification{ID: "n1", Priority: PriorityNormal, Class: ClassScoped, TargetScope: "project-9"}
	queues.enqueue(n)
	d.Drain(context.Background())

	if len(*got) != 0 {
		t.Fatal("subscriber outside the target scope received the notification")
	}
	_, counts := inbox.List(context.Background(), session("a", "project-1"), InboxOpts{})
	if counts.Withheld != 1 {
		t.Fatalf("Withheld count = %d, want 1", counts.Withheld)
	}
}

func TestDispatchExpiredNotDispatched(t *testing.T) {
	now := time.Unix(1000, 0)
	d, queues, registry, inbox := newTestDispatcher(t, now)
	sub, got := recordingSubscriber("a", true)
	registry.Subscribe(sub, session("a"))

	n := Notification{ID: "n1", Priority: PriorityNormal, Class: ClassGlobalCritical, ExpiresAt: now.Add(-time.Second)}
	queues.enqueue(n)
	d.Drain(context.Background())

	if len(*got) != 0 {
		t.Fatal("expired notification was dispatched")
	}
	_, counts := inbox.List(context.Background(), session("a"), InboxOpts{})
	if counts.Expired != 1 {
		t.Fatalf("Expired count = %d, want 1", counts.Expired)
	}
}

func TestDispatchPriorityOrder(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	var order []string
	var mu sync.Mutex
	sub := stubSubscriber{id: "a", matches: true, recv: func(n Notification) error {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, n.ID)
		return nil
	}}
	registry.Subscribe(sub, session("a"))

	queues.enqueue(Notification{ID: "low", Priority: PriorityLow, Class: ClassGlobalCritical})
	queues.enqueue(Notification{ID: "normal", Priority: PriorityNormal, Class: ClassGlobalCritical})
	queues.enqueue(Notification{ID: "urgent", Priority: PriorityUrgent, Class: ClassGlobalCritical})
	d.Drain(context.Background())

	if len(order) != 3 || order[0] != "urgent" || order[1] != "normal" || order[2] != "low" {
		t.Fatalf("dispatch order = %v, want [urgent normal low]", order)
	}
}

func TestFullQueueDropRecordsInboxAndEmits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferNormal = 1
	queues := newQueueSet(cfg, silentLogger())
	registry := NewRegistry()
	inbox := NewInbox()
	NewDispatcher(queues, registry, inbox, fixedClock{now: time.Unix(100, 0)}, silentLogger())

	var events []DropEvent
	queues.drops.setSink(func(_ context.Context, e DropEvent) { events = append(events, e) })

	queues.enqueue(Notification{ID: "first", Priority: PriorityNormal, Class: ClassGlobalCritical})
	queues.enqueue(Notification{ID: "second", Priority: PriorityNormal, Class: ClassGlobalCritical, TargetSession: "a", Visibility: VisibilityPrivate})

	if _, err := inbox.Read(context.Background(), session("a"), "second"); err != nil {
		t.Fatalf("dropped item not readable in the inbox: %v", err)
	}
	if got := queues.drops.count(DropQueueFull); got != 1 {
		t.Fatalf("queue-full drop count = %d, want 1", got)
	}
	if len(events) != 1 || events[0].ID != "second" || events[0].Reason != DropQueueFull {
		t.Fatalf("sink events = %+v, want one queue-full event for second", events)
	}
}

// TestAccumulationBufferBounded checks overflow uses the existing inbox and drop sink.
func TestAccumulationBufferBounded(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.BufferAccumulate != 4096 {
		t.Fatalf("default accumulation limit = %d, want 4096", cfg.BufferAccumulate)
	}
	cfg.BufferAccumulate = 2
	now := time.Unix(100, 0)
	svc := NewService(cfg, fixedClock{now: now}, silentLogger())
	queues := svc.Dispatcher.queues
	var events []DropEvent
	queues.drops.setSink(func(_ context.Context, e DropEvent) {
		if _, err := svc.Inbox.Read(context.Background(), session("a"), e.ID); err != nil {
			t.Fatalf("drop sink ran before inbox record: %v", err)
		}
		if queues.accumulatedLen() != 2 || queues.drops.count(e.Reason) != 1 {
			t.Fatal("drop sink observed incorrect buffer length or counter")
		}
		events = append(events, e)
	})
	svc.Router.SetAccumulate(true)
	for _, id := range []string{"first", "second", "overflow"} {
		err := svc.Notifier.Deliver(context.Background(), Notification{
			ID: id, Source: "test", Priority: PriorityNormal, Class: ClassAddressed,
			TargetSession: "a", Visibility: VisibilityPrivate, Payload: []byte("private body"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := svc.Dispatcher.Drain(context.Background()); got != 0 {
		t.Fatalf("drain processed %d accumulated items, want 0", got)
	}
	want := DropEvent{ID: "overflow", Source: "test", Priority: PriorityNormal, Reason: DropAccumulationFull, At: now}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("drop events = %+v, want %+v", events, want)
	}
	if queues.drops.count(DropQueueFull) != 0 {
		t.Fatal("accumulation overflow counted as queue-full")
	}
	items := svc.Router.DrainAccumulated()
	if len(items) != 2 || items[0].ID != "first" || items[1].ID != "second" || queues.accumulatedLen() != 0 {
		t.Fatal("buffer did not retain and drain exactly the first two items")
	}
}

// TestRetryBypassesAccumulation checks failures retry at the next drain while away.
func TestRetryBypassesAccumulation(t *testing.T) {
	d, queues, registry, _ := newTestDispatcher(t, time.Unix(100, 0))
	attempts := 0
	registry.Subscribe(stubSubscriber{id: "fail-once", matches: true, recv: func(Notification) error {
		attempts++
		if attempts == 1 {
			queues.setAccumulate(true)
			queues.enqueue(Notification{ID: "away", Priority: PriorityNormal})
			return errors.New("retry")
		}
		return nil
	}}, session("a"))
	queues.enqueue(Notification{ID: "retry", Priority: PriorityNormal, Class: ClassGlobalCritical})
	if got := d.Drain(context.Background()); got != 1 || attempts != 1 {
		t.Fatalf("first drain processed %d with %d attempts, want 1 each", got, attempts)
	}
	if !queues.isAccumulating() || queues.accumulatedLen() != 1 || len(queues.queues[PriorityNormal]) != 1 {
		t.Fatal("retry did not bypass the active accumulation gate")
	}
	if got := d.Drain(context.Background()); got != 1 || attempts != 2 {
		t.Fatalf("retry drain processed %d with %d attempts, want 1 and 2", got, attempts)
	}
	items := queues.drainAccumulated()
	if len(items) != 1 || items[0].ID != "away" {
		t.Fatal("retry changed the away buffer")
	}
	if queues.drops.count(DropAccumulationFull) != 0 || queues.drops.count(DropQueueFull) != 0 {
		t.Fatal("retry unexpectedly dropped")
	}
}
