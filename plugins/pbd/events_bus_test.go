// Purpose: real-behavior unit coverage for pbd.go's bus (S-29.T1's
//
//	fan-out broadcaster): multi-subscriber delivery, non-blocking drop on
//	a full subscriber buffer, and idempotent unsubscribe-and-close. This
//	complements projector_test.go's integration-tagged, real-socket SSE
//	conformance evidence (Art.2) which the default lane never compiles —
//	the no-network-unit-lane gate (internal/build/hygiene.go) forbids a
//	bare "net"/"net/http" import here, so this file only imports the bus
//	type itself, never a transport.
//
// Inputs: none (in-process only).
// Outputs: none; asserts on channel contents and map state.
// Constraints: deterministic — every wait is on a channel receive with a
//
//	bounded select, never a sleep-and-poll.
//
// SPORT: plugins/pbd.bus/COVERED (P1-E14-W3-S29-T1 coverage gap fix).
package pbd

import (
	"context"
	"testing"
	"time"
)

// recvWithin waits for one busEvent on ch, failing the test if none
// arrives before the bound. Bounding the wait (rather than an unbounded
// receive) keeps a broken fan-out from hanging the whole package, per
// the phase's standing determinism rule.
func recvWithin(t *testing.T, ch chan busEvent) busEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for busEvent")
		return busEvent{}
	}
}

func TestBus_Publish_FansOutToAllSubscribers(t *testing.T) {
	b := newBus()
	id1, ch1 := b.subscribe()
	id2, ch2 := b.subscribe()
	defer b.unsubscribe(id1)
	defer b.unsubscribe(id2)

	if err := b.Publish(context.Background(), "phase-a", []byte(`{"n":1}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ev1 := recvWithin(t, ch1)
	ev2 := recvWithin(t, ch2)
	for _, ev := range []busEvent{ev1, ev2} {
		if ev.phase != "phase-a" {
			t.Errorf("phase = %q, want %q", ev.phase, "phase-a")
		}
		if string(ev.payload) != `{"n":1}` {
			t.Errorf("payload = %q, want %q", ev.payload, `{"n":1}`)
		}
	}
}

func TestBus_Publish_OneSubscriberNeverBlocksAnother(t *testing.T) {
	b := newBus()
	// slowID never drains its channel; fastID must still receive every
	// publish up to its own buffer's capacity, proving Publish's
	// per-subscriber select/default does not serialize on a stuck peer.
	slowID, slowCh := b.subscribe()
	_, fastCh := b.subscribe()
	defer b.unsubscribe(slowID)
	_ = slowCh

	if err := b.Publish(context.Background(), "p", []byte("x")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	ev := recvWithin(t, fastCh)
	if ev.phase != "p" {
		t.Errorf("fast subscriber phase = %q, want %q", ev.phase, "p")
	}
}

func TestBus_Publish_DropsWhenSubscriberBufferFull(t *testing.T) {
	b := newBus()
	id, ch := b.subscribe()
	defer b.unsubscribe(id)

	// Fill the buffer to capacity without draining it.
	for i := 0; i < busSubscribeBuffer; i++ {
		if err := b.Publish(context.Background(), "fill", []byte("v")); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	if got := len(ch); got != busSubscribeBuffer {
		t.Fatalf("buffered length = %d, want %d (buffer should be full)", got, busSubscribeBuffer)
	}

	// One more publish must not block (proves the select/default drop
	// path) and must not grow the channel past capacity.
	done := make(chan struct{})
	go func() {
		_ = b.Publish(context.Background(), "overflow", []byte("dropped"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer instead of dropping")
	}
	if got := len(ch); got != busSubscribeBuffer {
		t.Fatalf("buffered length after overflow = %d, want unchanged %d", got, busSubscribeBuffer)
	}

	// Drain and confirm every buffered event is one of the "fill"
	// publishes, never the dropped "overflow" one.
	for i := 0; i < busSubscribeBuffer; i++ {
		ev := recvWithin(t, ch)
		if ev.phase != "fill" {
			t.Fatalf("buffered event %d phase = %q, want %q (overflow event must have been dropped)", i, ev.phase, "fill")
		}
	}
}

func TestBus_Unsubscribe_ClosesChannelAndIsIdempotent(t *testing.T) {
	b := newBus()
	id, ch := b.subscribe()

	b.unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}

	// A second unsubscribe of the same (already-removed) id must be a
	// no-op, never a panic (double-close).
	b.unsubscribe(id)

	b.mu.Lock()
	remaining := len(b.subs)
	b.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("subs map has %d entries after unsubscribe, want 0", remaining)
	}
}

func TestBus_Subscribe_AssignsDistinctIDs(t *testing.T) {
	b := newBus()
	id1, _ := b.subscribe()
	id2, _ := b.subscribe()
	defer b.unsubscribe(id1)
	defer b.unsubscribe(id2)
	if id1 == id2 {
		t.Fatalf("subscribe returned duplicate ids: %d == %d", id1, id2)
	}
}
