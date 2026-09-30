// Purpose: SubscribeFromHead and ResetCursorToHead. A consumer configured
//
//	after history exists must not replay it, a restarted consumer with a
//	committed cursor must still get its backlog, and a reset drops the
//	backlog and refuses while the cursor is live.
//
// Constraints: white-box (package events) so the persisted cursor value is
//
//	read from the store, not inferred from delivery alone.
package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// nextEvent receives one event or fails after a bounded wait.
func nextEvent(t *testing.T, sub *Subscription) Event {
	t.Helper()
	select {
	case ev, ok := <-sub.Events:
		if !ok {
			t.Fatal("subscription closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event delivered")
	}
	return Event{}
}

func publishN(t *testing.T, bus *Bus, ns string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := bus.Publish(context.Background(), ns, "k", "s", nil); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
}

// TestSubscribeFromHeadSkipsHistory publishes 100 events before the first
// subscribe: the stored cursor is committed at 100 before delivery, and
// the first event delivered is 101.
func TestSubscribeFromHeadSkipsHistory(t *testing.T) {
	bus, store := newCauseBus(t)
	ctx := context.Background()
	publishN(t, bus, "ns", 100)
	sub, err := bus.SubscribeFromHead(ctx, "ns", "c", 4)
	if err != nil {
		t.Fatalf("SubscribeFromHead: %v", err)
	}
	if cur, err := loadCursor(ctx, store, "ns", "c"); err != nil || cur != 100 {
		t.Fatalf("stored cursor = %d, %v; want 100 committed before delivery", cur, err)
	}
	publishN(t, bus, "ns", 1)
	if ev := nextEvent(t, sub); ev.Seq != 101 {
		t.Fatalf("first delivered seq = %d, want 101 (history replayed)", ev.Seq)
	}
	// Control: plain Subscribe on a fresh cursor still replays from 0.
	plain, err := bus.Subscribe(ctx, "ns", "plain", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if ev := nextEvent(t, plain); ev.Seq != 1 {
		t.Fatalf("plain Subscribe first seq = %d, want 1", ev.Seq)
	}
}

// TestSubscribeFromHeadResumesCommittedCursor proves a committed cursor is
// honoured: a restarted subscriber gets the backlog published while it was
// away, in order.
func TestSubscribeFromHeadResumesCommittedCursor(t *testing.T) {
	bus, _ := newCauseBus(t)
	ctx := context.Background()
	sub, err := bus.SubscribeFromHead(ctx, "ns", "c", 4)
	if err != nil {
		t.Fatalf("SubscribeFromHead: %v", err)
	}
	publishN(t, bus, "ns", 2)
	nextEvent(t, sub)
	nextEvent(t, sub)
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	publishN(t, bus, "ns", 3)
	again, err := bus.SubscribeFromHead(ctx, "ns", "c", 4)
	if err != nil {
		t.Fatalf("SubscribeFromHead (restart): %v", err)
	}
	for want := uint64(3); want <= 5; want++ {
		if ev := nextEvent(t, again); ev.Seq != want {
			t.Fatalf("backlog seq = %d, want %d", ev.Seq, want)
		}
	}
}

// TestResetCursorToHeadDropsBacklog proves the reset commits the head in
// the store, the next subscribe skips the backlog, and a live cursor is
// refused with KindConflict (identity and message checked).
func TestResetCursorToHeadDropsBacklog(t *testing.T) {
	bus, store := newCauseBus(t)
	ctx := context.Background()
	sub, err := bus.Subscribe(ctx, "ns", "c", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	err = bus.ResetCursorToHead(ctx, "ns", "c")
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindConflict ||
		!strings.Contains(err.Error(), `cursor "c" has an active subscription and cannot be reset`) {
		t.Fatalf("reset of a live cursor = %v, want KindConflict naming the cursor", err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	publishN(t, bus, "ns", 50)
	if cur, _ := loadCursor(ctx, store, "ns", "c"); cur != 0 {
		t.Fatalf("cursor moved without delivery: %d", cur)
	}
	if err := bus.ResetCursorToHead(ctx, "ns", "c"); err != nil {
		t.Fatalf("ResetCursorToHead: %v", err)
	}
	if cur, err := loadCursor(ctx, store, "ns", "c"); err != nil || cur != 50 {
		t.Fatalf("stored cursor after reset = %d, %v; want 50", cur, err)
	}
	again, err := bus.Subscribe(ctx, "ns", "c", 4)
	if err != nil {
		t.Fatalf("Subscribe after reset: %v", err)
	}
	publishN(t, bus, "ns", 1)
	if ev := nextEvent(t, again); ev.Seq != 51 {
		t.Fatalf("first seq after reset = %d, want 51", ev.Seq)
	}
	_ = bus.Close()
	if err := bus.ResetCursorToHead(ctx, "ns", "c"); err == nil {
		t.Fatal("ResetCursorToHead after Close succeeded")
	}
}
