// Purpose: the backpressure boundary test bus.go's package doc promises —
//
//	a slow/dead subscriber's bounded channel fills, its delivery goroutine
//	blocks (never drops, never grows unboundedly), and Unsubscribe tears
//	it down deterministically with zero data loss: undelivered events stay
//	in the durable log for the next Subscribe under the same cursor name.
//	R-14.117-authorized split.
//
// SPORT: internal.events.Bus/ADDED (backpressure test) (P1-E03-W1-S04-T3).
package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestBus_Backpressure_SlowOrDeadSubscriber_BoundedNoLoss(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	// bufferSize 0 (unbuffered/synchronous channel): the delivery
	// goroutine's send only completes when this test actually receives,
	// so "drain exactly one, then stop" deterministically leaves the
	// goroutine blocked trying to send the NEXT event, with nothing
	// ambiguously sitting pre-buffered.
	sub, err := bus.Subscribe(ctx, "ns", "consumer-a", 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := bus.Publish(ctx, "ns", "k", "src", []byte{byte(i)}); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
	}

	// Drain exactly one event, then stop reading — the subscriber goes
	// "slow/dead." Its delivery goroutine can now only be blocked trying
	// to send event 2 into a full, size-1 buffer: not dropped (no data
	// loss, proven below), not growing without bound (the channel's own
	// capacity is the bound).
	first := <-sub.Events
	if first.Seq != 1 {
		t.Fatalf("first delivered Seq = %d, want 1", first.Seq)
	}

	// Unsubscribe must return promptly and deterministically — no sleep,
	// no poll — even though the delivery goroutine is presently blocked
	// on a send nobody will ever read. This is the leak-freedom proof:
	// Unsubscribe always terminates the goroutine.
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe on a stalled subscriber: %v", err)
	}
	if _, open := <-sub.Events; open {
		t.Fatal("Events still open after Unsubscribe")
	}

	// No data loss: events 2 and 3 were never dropped — they are still in
	// the durable log, and a fresh Subscribe under the SAME cursor name
	// resumes exactly after the one event that was actually delivered
	// (only Seq 1's send ever completed, so only Seq 1 was committed).
	resumed, err := bus.Subscribe(ctx, "ns", "consumer-a", 8)
	if err != nil {
		t.Fatalf("re-Subscribe: %v", err)
	}
	for i, wantSeq := range []uint64{2, 3} {
		ev := <-resumed.Events
		if ev.Seq != wantSeq {
			t.Fatalf("resumed event %d Seq = %d, want %d (no data loss, no premature commit)", i, ev.Seq, wantSeq)
		}
	}
}
