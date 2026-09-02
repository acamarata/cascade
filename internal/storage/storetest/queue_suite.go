// Purpose: RunQueueTests — the provider.Queue conformance suite, including
//   the enqueue-overflow and ack-timeout error paths.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// BoundedQueue is implemented by a Queue driver whose Enqueue capacity can
// be exhausted deterministically. RunQueueTests type-asserts the driver
// returned by its factory against this interface and, when it is
// satisfied, additionally runs the enqueue-overflow error-path case
// (drivers with no fixed capacity, e.g. an unbounded in-memory reference,
// legitimately do not implement it).
type BoundedQueue interface {
	// Capacity returns the maximum number of un-Acked messages namespace
	// may hold before Enqueue starts returning cascade.KindUnavailable.
	Capacity(namespace string) int
}

// RunQueueTests exercises every provider.Queue method, including the
// ack-timeout error path (always) and the enqueue-overflow error path
// (when the driver implements BoundedQueue), against a driver produced by
// newQueue.
func RunQueueTests(t *testing.T, newQueue QueueFactory) {
	t.Helper()
	t.Run("EnqueueDequeueAck", func(t *testing.T) { testQueueEnqueueDequeueAck(t, newQueue(t)) })
	t.Run("DequeueEmptyIsNilNil", func(t *testing.T) { testQueueDequeueEmpty(t, newQueue(t)) })
	t.Run("Nack", func(t *testing.T) { testQueueNack(t, newQueue(t)) })
	t.Run("AckTimeout", func(t *testing.T) { testQueueAckTimeout(t, newQueue(t)) })
	t.Run("EnqueueOverflow", func(t *testing.T) { testQueueEnqueueOverflow(t, newQueue(t)) })
}

func testQueueEnqueueDequeueAck(t *testing.T, q provider.Queue) {
	t.Helper()
	ctx := testContext(t)
	id, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoError(t, err, "Enqueue")
	if id == "" {
		t.Fatal("Enqueue returned empty id")
	}
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoError(t, err, "Dequeue")
	if msg == nil {
		t.Fatal("Dequeue = nil, want a message")
	}
	if msg.ID != id {
		t.Fatalf("Dequeue id = %q, want %q", msg.ID, id)
	}
	requireBytesEqual(t, msg.Payload, []byte("payload"), "Dequeue payload")
	requireNoError(t, q.Ack(ctx, "ns", msg.Receipt), "Ack")
}

func testQueueDequeueEmpty(t *testing.T, q provider.Queue) {
	t.Helper()
	ctx := testContext(t)
	msg, err := q.Dequeue(ctx, "empty-ns", time.Minute)
	requireNoError(t, err, "Dequeue of empty namespace")
	if msg != nil {
		t.Fatalf("Dequeue of empty namespace = %+v, want nil", msg)
	}
}

func testQueueNack(t *testing.T, q provider.Queue) {
	t.Helper()
	ctx := testContext(t)
	_, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoError(t, err, "Enqueue")
	msg, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoError(t, err, "Dequeue")
	if msg == nil {
		t.Fatal("Dequeue = nil, want a message")
	}
	requireNoError(t, q.Nack(ctx, "ns", msg.Receipt), "Nack")
	redelivered, err := q.Dequeue(ctx, "ns", time.Minute)
	requireNoError(t, err, "Dequeue after Nack")
	if redelivered == nil || redelivered.ID != msg.ID {
		t.Fatalf("Dequeue after Nack = %+v, want redelivery of id %q", redelivered, msg.ID)
	}
}

// testQueueAckTimeout exercises the ack-timeout error path: a message
// Dequeued with a near-zero visibility timeout becomes eligible for
// redelivery almost immediately, so Ack against the original (now stale)
// receipt must fail with cascade.KindTimeout once redelivery has happened.
func testQueueAckTimeout(t *testing.T, q provider.Queue) {
	t.Helper()
	ctx := testContext(t)
	_, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoError(t, err, "Enqueue")
	first, err := q.Dequeue(ctx, "ns", time.Nanosecond)
	requireNoError(t, err, "first Dequeue")
	if first == nil {
		t.Fatal("first Dequeue = nil, want a message")
	}
	redelivered := pollForRedelivery(t, q, "ns")
	err = q.Ack(ctx, "ns", first.Receipt)
	requireErrorKind(t, err, cascade.KindTimeout, "Ack with stale receipt after redelivery")
	requireNoError(t, q.Ack(ctx, "ns", redelivered.Receipt), "Ack with current receipt")
}

// pollForRedelivery polls Dequeue on namespace until a message reappears
// (its visibility having already elapsed) or the deadline passes.
func pollForRedelivery(t *testing.T, q provider.Queue, namespace string) *provider.Message {
	t.Helper()
	ctx := testContext(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := q.Dequeue(ctx, namespace, time.Minute)
		requireNoError(t, err, "polling Dequeue for redelivery")
		if msg != nil {
			return msg
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("message with expired visibility was never redelivered")
	return nil
}

// testQueueEnqueueOverflow exercises the enqueue-overflow error path when
// the driver reports a bounded Capacity; it is a no-op for unbounded
// drivers.
func testQueueEnqueueOverflow(t *testing.T, q provider.Queue) {
	t.Helper()
	bounded, ok := q.(BoundedQueue)
	if !ok {
		t.Skip("driver does not implement BoundedQueue; enqueue-overflow is not applicable")
	}
	ctx := testContext(t)
	capacity := bounded.Capacity("ns")
	if capacity <= 0 {
		t.Skip("driver reports non-positive capacity; enqueue-overflow is not applicable")
	}
	for i := 0; i < capacity; i++ {
		_, err := q.Enqueue(ctx, "ns", []byte("fill"))
		requireNoError(t, err, "Enqueue filling capacity")
	}
	_, err := q.Enqueue(ctx, "ns", []byte("overflow"))
	requireErrorKind(t, err, cascade.KindUnavailable, "Enqueue past capacity")
}
