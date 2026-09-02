// Purpose: Queue conformance (via storetest.RunQueueTests, run against
//   storetest.NewMemStore per the ticket, including the ack-timeout and
//   enqueue-overflow error paths via the BoundedQueue upcast) plus
//   driver-specific edge cases: visibility-timeout re-queue with a
//   deterministic clock, retry-cap DLQ promotion, and a concurrent
//   producer/consumer stress test under -race.
// Constraints: no sleeps as synchronization in THIS file's own tests
//   (Art.7.3/Art.11). Per R-14.136, the conformance factory below passes
//   storetest.WithQueueClock(clock) with the SAME testkit.FrozenClock the
//   Queue is constructed with, so storetest's AckTimeout case drives
//   ack-timeout by advancing that clock directly rather than sleeping and
//   polling — no real time elapses in TestQueue_Conformance at all.
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).

package queue_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/queue"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestQueue_Conformance(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	storetest.RunQueueTests(t, func(t *testing.T) provider.Queue {
		t.Helper()
		return queue.New(storetest.NewMemStore(), clock, queue.Config{Capacity: 8})
	}, storetest.WithQueueClock(clock))
}

func TestQueue_VisibilityTimeoutRequeue_DeterministicClock(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	q := queue.New(storetest.NewMemStore(), clock, queue.Config{})

	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	first, err := q.Dequeue(ctx, "ns", time.Minute)
	if err != nil {
		t.Fatalf("first Dequeue: %v", err)
	}
	if first == nil || first.ID != id {
		t.Fatalf("first Dequeue = %+v, want id %q", first, id)
	}

	// Before the visibility timeout elapses, the queue must stay empty.
	if again, err := q.Dequeue(ctx, "ns", time.Minute); err != nil || again != nil {
		t.Fatalf("Dequeue before timeout elapsed: msg=%+v err=%v, want nil,nil", again, err)
	}

	clock.Advance(2 * time.Minute)

	redelivered, err := q.Dequeue(ctx, "ns", time.Minute)
	if err != nil {
		t.Fatalf("Dequeue after timeout elapsed: %v", err)
	}
	if redelivered == nil || redelivered.ID != id {
		t.Fatalf("Dequeue after timeout elapsed = %+v, want redelivery of id %q", redelivered, id)
	}
	if redelivered.Receipt == first.Receipt {
		t.Fatal("redelivered message kept the same receipt as the original claim, want a fresh one")
	}

	if err := q.Ack(ctx, "ns", redelivered.Receipt); err != nil {
		t.Fatalf("Ack with current receipt: %v", err)
	}
}

func TestQueue_RetryCapPromotesToDLQ(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()
	q := queue.New(store, clock, queue.Config{MaxAttempts: 2})

	id, err := q.Enqueue(ctx, "ns", []byte("doomed"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Two claims, each timing out without an Ack, exhausts MaxAttempts=2.
	for i := 0; i < 2; i++ {
		msg, err := q.Dequeue(ctx, "ns", time.Nanosecond)
		if err != nil {
			t.Fatalf("Dequeue attempt %d: %v", i+1, err)
		}
		if msg == nil || msg.ID != id {
			t.Fatalf("Dequeue attempt %d = %+v, want id %q", i+1, msg, id)
		}
		clock.Advance(time.Minute) // let the visibility timeout elapse
	}

	// A third Dequeue must find nothing ready: the message was
	// dead-lettered instead of being redelivered a third time.
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	if err != nil {
		t.Fatalf("Dequeue after DLQ promotion: %v", err)
	}
	if msg != nil {
		t.Fatalf("Dequeue after DLQ promotion = %+v, want nil (message should be dead-lettered)", msg)
	}

	// The record now lives under the dlq: prefix in the same Store the
	// test constructed, not under msg:.
	it, err := store.Scan(ctx, "ns", "dlq:")
	if err != nil {
		t.Fatalf("Scan dlq: prefix: %v", err)
	}
	defer func() { _ = it.Close() }()
	if !it.Next(ctx) {
		t.Fatal("no dlq: record found after MaxAttempts exhaustion")
	}
}

func TestQueue_AckUnknownReceipt(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	q := queue.New(storetest.NewMemStore(), clock, queue.Config{})

	err := q.Ack(ctx, "ns", "never-issued")
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("Ack of unknown receipt: want KindTimeout, got %v", err)
	}
}

const (
	concurrentTestMessages  = 200
	concurrentTestProducers = 4
	concurrentTestConsumers = 4
)

func TestQueue_ConcurrentProducerConsumer(t *testing.T) {
	q := queue.New(storetest.NewMemStore(), runtime.NewSystemClock(), queue.Config{})

	runProducers(t, q)

	var acked atomic64
	var consumersWG sync.WaitGroup
	consumersWG.Add(concurrentTestConsumers)
	for c := 0; c < concurrentTestConsumers; c++ {
		go consumeUntilDrained(t, q, &acked, &consumersWG)
	}
	consumersWG.Wait()

	if got := acked.load(); got != concurrentTestMessages {
		t.Fatalf("acked %d messages, want %d", got, concurrentTestMessages)
	}
}

// runProducers enqueues concurrentTestMessages messages across
// concurrentTestProducers goroutines and waits for all of them to finish.
func runProducers(t *testing.T, q *queue.Queue) {
	t.Helper()
	ctx := context.Background()
	perProducer := concurrentTestMessages / concurrentTestProducers
	var produced sync.WaitGroup
	produced.Add(concurrentTestProducers)
	for p := 0; p < concurrentTestProducers; p++ {
		go func() {
			defer produced.Done()
			for i := 0; i < perProducer; i++ {
				if _, err := q.Enqueue(ctx, "ns", []byte("payload")); err != nil {
					t.Errorf("Enqueue: %v", err)
				}
			}
		}()
	}
	produced.Wait()
}

// consumeUntilDrained repeatedly Dequeues and Acks until acked reaches
// concurrentTestMessages, then signals wg and returns.
func consumeUntilDrained(t *testing.T, q *queue.Queue, acked *atomic64, wg *sync.WaitGroup) {
	t.Helper()
	defer wg.Done()
	ctx := context.Background()
	for {
		msg, err := q.Dequeue(ctx, "ns", time.Minute)
		if err != nil {
			t.Errorf("Dequeue: %v", err)
			return
		}
		if msg == nil {
			if acked.load() >= concurrentTestMessages {
				return
			}
			continue
		}
		if err := q.Ack(ctx, "ns", msg.Receipt); err != nil {
			t.Errorf("Ack: %v", err)
			return
		}
		if acked.add(1) >= concurrentTestMessages {
			return
		}
	}
}

// atomic64 is a tiny goroutine-safe counter, avoiding a sync/atomic import
// footprint for a single test's bookkeeping.
type atomic64 struct {
	mu sync.Mutex
	n  int
}

func (a *atomic64) add(delta int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n += delta
	return a.n
}

func (a *atomic64) load() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
