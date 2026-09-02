// Purpose: the concurrency proof under -race the ticket calls for: multiple
//
//	publishers and subscribers, Subscribe/Unsubscribe racing ongoing
//	delivery, and Close racing in-flight Publish calls. R-14.117-authorized
//	split. No sleeps (Art.11) — every synchronization point is a real
//	channel operation or sync.WaitGroup.
//
// SPORT: internal.events.Bus/ADDED (concurrency tests) (P1-E03-W1-S04-T3).
package events_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestBus_Concurrency_MultiPublisherSingleSubscriber runs many concurrent
// publishers against one namespace and proves the single subscriber still
// observes every event exactly once, in strict Seq order — the ordering
// and no-duplication guarantee must hold under real concurrent writers,
// not just a single goroutine.
func TestBus_Concurrency_MultiPublisherSingleSubscriber(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	const publishers = 8
	const perPublisher = 50
	const total = publishers * perPublisher

	sub, err := bus.Subscribe(ctx, "ns", "consumer", total)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				if _, perr := bus.Publish(ctx, "ns", "k", fmt.Sprintf("pub-%d", p), []byte{byte(i)}); perr != nil {
					t.Errorf("Publish: %v", perr)
				}
			}
		}(p)
	}
	wg.Wait()

	seen := make(map[uint64]bool, total)
	var lastSeq uint64
	for i := 0; i < total; i++ {
		ev := <-sub.Events
		if seen[ev.Seq] {
			t.Fatalf("duplicate delivery of Seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
		if ev.Seq <= lastSeq {
			t.Fatalf("out-of-order delivery: Seq %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
	}
	if len(seen) != total {
		t.Fatalf("delivered %d unique events, want %d", len(seen), total)
	}
}

// TestBus_Concurrency_SubscribeUnsubscribeDuringDelivery races many
// Subscribe/partial-drain/Unsubscribe cycles against ongoing Publish
// calls, proving neither side corrupts the other under -race.
func TestBus_Concurrency_SubscribeUnsubscribeDuringDelivery(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	const consumers = 20
	const drainPerConsumer = 3

	var wg sync.WaitGroup
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("consumer-%d", i)
			sub, serr := bus.Subscribe(ctx, "ns", name, 4)
			if serr != nil {
				t.Errorf("Subscribe %s: %v", name, serr)
				return
			}
			for j := 0; j < drainPerConsumer; j++ {
				select {
				case _, ok := <-sub.Events:
					if !ok {
						return
					}
				case <-sub.Errs:
					return
				}
			}
			if uerr := sub.Unsubscribe(); uerr != nil {
				t.Errorf("Unsubscribe %s: %v", name, uerr)
			}
		}(i)
	}

	var pubWG sync.WaitGroup
	pubWG.Add(1)
	go func() {
		defer pubWG.Done()
		for i := 0; i < 200; i++ {
			if _, perr := bus.Publish(ctx, "ns", "k", "src", nil); perr != nil {
				t.Errorf("Publish: %v", perr)
			}
		}
	}()

	pubWG.Wait()
	wg.Wait()
}

// TestBus_Concurrency_CloseWhileInFlight races Bus.Close against an
// ongoing publisher and proves Close still terminates every subscription
// deterministically (Events closed) and Publish afterward fails cleanly
// rather than panicking or deadlocking.
func TestBus_Concurrency_CloseWhileInFlight(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)

	// bufferSize 0: after draining exactly one event below, the delivery
	// goroutine is guaranteed to be blocked mid-send (not resting with a
	// spare buffered item), so Close's termination is unambiguous.
	sub, err := bus.Subscribe(ctx, "ns", "consumer", 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _ = bus.Publish(ctx, "ns", "k", "src", nil)
		}
	}()

	<-sub.Events // make sure delivery has genuinely started

	if err := bus.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()

	if _, open := <-sub.Events; open {
		t.Fatal("Events still open after Close")
	}
	if _, perr := bus.Publish(ctx, "ns", "k", "src", nil); perr == nil {
		t.Fatal("Publish after Close returned nil error, want a typed failure")
	}
}
