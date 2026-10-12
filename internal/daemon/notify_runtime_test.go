package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// chanTicker is a Ticker the test fires by hand.
type chanTicker struct {
	c       chan struct{}
	once    sync.Once
	stopped chan struct{}
}

func newChanTicker() *chanTicker {
	return &chanTicker{c: make(chan struct{}), stopped: make(chan struct{})}
}

func (t *chanTicker) C() <-chan struct{} { return t.c }
func (t *chanTicker) Stop()              { t.once.Do(func() { close(t.stopped) }) }

type recordingSub struct {
	got chan notify.Notification
}

func (s recordingSub) ID() string                     { return "rec" }
func (s recordingSub) Match(notify.Notification) bool { return true }
func (s recordingSub) Receive(n notify.Notification) error {
	s.got <- n
	return nil
}

func newTestNotifyRuntime(t *testing.T) (*NotifyRuntime, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))
	svc := notify.NewService(notify.DefaultConfig(), clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return NewNotifyRuntime(svc), clock
}

func TestNotifyRuntimeDispatcherDrainsPerTick(t *testing.T) {
	rt, _ := newTestNotifyRuntime(t)
	rec := recordingSub{got: make(chan notify.Notification, 1)}
	rt.Service().Registry.Subscribe(rec, notify.CandidateSession{SessionID: "s"})
	err := rt.Service().Notifier.Deliver(context.Background(),
		notify.Notification{ID: "g-1", Class: notify.ClassGlobalCritical, Priority: notify.PriorityUrgent})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	ticker := newChanTicker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.RunDispatcher(ctx, ticker) }()
	ticker.c <- struct{}{}
	select {
	case n := <-rec.got:
		if n.ID != "g-1" {
			t.Fatalf("drained %q, want g-1", n.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a tick did not drain the queued notification")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunDispatcher after cancel = %v, want nil", err)
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("RunDispatcher returned without stopping its ticker")
	}
}

func TestNotifyRuntimeRouterRoutesNotifyNamespace(t *testing.T) {
	rt, clock := newTestNotifyRuntime(t)
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.RunRouter(ctx, bus) }()
	payload := events.EncodeNotificationPayload(events.NotificationPayload{ID: "bus-1"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		// The router subscribes from head: publish until one lands after it.
		if _, err := bus.Publish(ctx, NotifyNamespace, "node.offline", "test", payload); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if rt.Service().Dispatcher.Drain(ctx) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no event published on the notify namespace reached the dispatcher")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunRouter after cancel = %v, want nil", err)
	}
}

type failingSubscriber struct{ err error }

func (f failingSubscriber) SubscribeFromHead(context.Context, string, string, int) (*events.Subscription, error) {
	return nil, f.err
}

func TestNotifyRuntimeRouterReportsSubscribeFailure(t *testing.T) {
	rt, _ := newTestNotifyRuntime(t)
	want := errors.New("bus closed")
	if err := rt.RunRouter(context.Background(), failingSubscriber{err: want}); err != want { //nolint:errorlint // identity
		t.Fatalf("RunRouter = %v, want the subscribe error", err)
	}
}
