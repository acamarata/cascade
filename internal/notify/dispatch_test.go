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
