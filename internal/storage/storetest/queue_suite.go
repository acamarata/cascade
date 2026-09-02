// Purpose: RunQueueTests — the provider.Queue conformance suite, including
//   the enqueue-overflow and ack-timeout error paths.
// Constraints: R-14.136 — the AckTimeout case must not sleep as its only
//   path to determinism. WithQueueClock lets a driver author who already
//   injects a runtime.Clock (or a testkit.Clock) hand that SAME instance to
//   the suite, which then drives ack-timeout by advancing it directly. A
//   driver author who supplies no clock keeps today's real-time poll
//   fallback (pollForRedelivery) — existing callers are unaffected.
// SPORT: internal.storage.storetest/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// AdvanceableClock is the subset of a Clock the suite needs to drive
// ack-timeout deterministically: the ability to move it forward by a
// duration and report the resulting instant. internal/runtime.FixedClock
// and internal/testkit.FrozenClock both satisfy this structurally
// (R-14.126) — pass either one via WithQueueClock.
type AdvanceableClock interface {
	// Advance moves the clock forward by d and returns the new instant.
	Advance(d time.Duration) time.Time
}

// queueOptions holds RunQueueTests' optional configuration, built from the
// QueueOption values passed to it.
type queueOptions struct {
	clock AdvanceableClock
}

// QueueOption configures RunQueueTests. See WithQueueClock.
type QueueOption func(*queueOptions)

// WithQueueClock supplies the SAME clock instance the driver returned by
// RunQueueTests' factory was constructed with. When set, the AckTimeout
// case advances this clock past the visibility timeout instead of sleeping
// and polling real elapsed time (R-14.136) — deterministic and fast rather
// than merely "usually fast enough".
//
// The clock passed here must be the one the driver itself reads Now()
// through (or a clock reading the same underlying state) — the suite only
// calls Advance on it; it never touches the driver directly. Passing an
// unrelated clock does not fall back to real-time polling: the driver
// never observes the advance, so the redelivery the case expects never
// happens and the case fails. A driver that wants the deterministic path
// must expose a clock seam and share it here; a driver with no such seam
// should omit this option and keep the real-time fallback.
func WithQueueClock(clock AdvanceableClock) QueueOption {
	return func(o *queueOptions) { o.clock = clock }
}

// BoundedQueue is implemented by a Queue driver whose Enqueue capacity can
// be exhausted deterministically. RunQueueTests type-asserts the driver
// returned by its factory against this interface and, when it is
// satisfied, additionally runs the enqueue-overflow error-path case
// (drivers with no fixed capacity, e.g. an unbounded in-memory reference,
// legitimately do not implement it).
type BoundedQueue interface {
	// Capacity returns the maximum number of un-Acked messages namespace
	// may hold before Enqueue starts returning cascade.KindQuotaExhausted.
	Capacity(namespace string) int
}

// RunQueueTests exercises every provider.Queue method, including the
// ack-timeout error path (always) and the enqueue-overflow error path
// (when the driver implements BoundedQueue), against a driver produced by
// newQueue. Pass WithQueueClock to drive ack-timeout deterministically
// (R-14.136); without it, ack-timeout falls back to real-time polling.
func RunQueueTests(t *testing.T, newQueue QueueFactory, opts ...QueueOption) {
	t.Helper()
	var cfg queueOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.clock == nil {
		// Say so out loud. R-14.136 removed the sleep from the ack-timeout
		// case, but only for drivers that pass a clock; the real-time
		// fallback below still polls and sleeps. A driver author who never
		// passes WithQueueClock would otherwise inherit that flake silently
		// and believe the suite was deterministic, which is exactly how the
		// original flake propagated in the first place.
		t.Logf("storetest: no WithQueueClock supplied — AckTimeout runs the " +
			"real-time polling fallback and is NOT deterministic. Expose a " +
			"clock seam on the driver and pass the same instance via " +
			"WithQueueClock (R-14.136, see README).")
	}
	t.Run("EnqueueDequeueAck", func(t *testing.T) { testQueueEnqueueDequeueAck(t, newQueue(t)) })
	t.Run("DequeueEmptyIsNilNil", func(t *testing.T) { testQueueDequeueEmpty(t, newQueue(t)) })
	t.Run("Nack", func(t *testing.T) { testQueueNack(t, newQueue(t)) })
	t.Run("AckTimeout", func(t *testing.T) { testQueueAckTimeout(t, newQueue(t), cfg.clock) })
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
// whose visibility timeout has elapsed becomes eligible for redelivery, so
// Ack against the original (now stale) receipt must fail with
// cascade.KindTimeout once redelivery has happened.
//
// When clock is non-nil (WithQueueClock), elapsed time is simulated by
// advancing clock directly — no sleep, no poll (R-14.136). When clock is
// nil, the case falls back to a near-zero visibility timeout and polls
// real elapsed time via pollForRedelivery, preserving the suite's
// pre-R-14.136 behavior for callers that supply no clock seam.
func testQueueAckTimeout(t *testing.T, q provider.Queue, clock AdvanceableClock) {
	t.Helper()
	ctx := testContext(t)
	_, err := q.Enqueue(ctx, "ns", []byte("payload"))
	requireNoError(t, err, "Enqueue")

	if clock != nil {
		testQueueAckTimeoutDeterministic(t, q, clock)
		return
	}

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

// testQueueAckTimeoutDeterministic is the WithQueueClock path of
// testQueueAckTimeout: it Dequeues with a normal (non-near-zero)
// visibility timeout, advances clock well past it, and asserts the
// message is redelivered on the very next Dequeue call — no polling loop,
// because the driver's own next call observes the advanced clock
// immediately.
func testQueueAckTimeoutDeterministic(t *testing.T, q provider.Queue, clock AdvanceableClock) {
	t.Helper()
	ctx := testContext(t)
	const visibilityTimeout = time.Minute
	first, err := q.Dequeue(ctx, "ns", visibilityTimeout)
	requireNoError(t, err, "first Dequeue")
	if first == nil {
		t.Fatal("first Dequeue = nil, want a message")
	}
	clock.Advance(2 * visibilityTimeout)
	redelivered, err := q.Dequeue(ctx, "ns", visibilityTimeout)
	requireNoError(t, err, "Dequeue after advancing clock past visibility timeout")
	if redelivered == nil {
		t.Fatal("Dequeue after advancing clock past visibility timeout = nil, want redelivery")
	}
	err = q.Ack(ctx, "ns", first.Receipt)
	requireErrorKind(t, err, cascade.KindTimeout, "Ack with stale receipt after redelivery")
	requireNoError(t, q.Ack(ctx, "ns", redelivered.Receipt), "Ack with current receipt")
}

// pollForRedelivery is the no-clock fallback path (see WithQueueClock):
// when a driver author supplies no AdvanceableClock, this polls Dequeue on
// namespace until a message reappears (its visibility having already
// elapsed) or maxPollAttempts is exhausted. It counts attempts rather than
// reading a wall-clock deadline so this
// non-test-file helper (storetest.go's own doc comment: this package is a
// library, not suffixed _test.go) stays clear of the bare-time.Now ban
// (.golangci.yml forbidigo; 02-TARGET-STRUCTURE.md §v1.1) — a bounded
// attempt count with a fixed per-attempt sleep is deterministic under
// -shuffle and needs no injected Clock.
const maxPollAttempts = 5000

func pollForRedelivery(t *testing.T, q provider.Queue, namespace string) *provider.Message {
	t.Helper()
	ctx := testContext(t)
	for i := 0; i < maxPollAttempts; i++ {
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
	requireErrorKind(t, err, cascade.KindQuotaExhausted, "Enqueue past capacity")
}
