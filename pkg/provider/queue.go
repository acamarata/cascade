// Purpose: declare the Queue family contract — an at-least-once work queue
//   with visibility-timeout-based redelivery, per the ticket text.
// Inputs: a namespace + payload (Enqueue), a visibility timeout (Dequeue),
//   a receipt handle (Ack/Nack).
// Outputs: a Message (Dequeue) or a pkg/cascade taxonomy error.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); any
//   timeout/expiry logic a driver implements for visibility timeouts must
//   use an injected clock (internal/runtime.Clock) rather than a bare
//   time.Now — this interface itself carries no clock (it is duration-based
//   only), leaving clock injection to each driver's construction.
// SPORT: pkg.provider.Queue/ADDED (P1-E02-W1-S02-T1).

package provider

import (
	"context"
	"time"
)

// Queue is an at-least-once work queue: a Dequeue makes a message
// invisible to other consumers for visibilityTimeout rather than removing
// it, so a consumer that crashes before Ack lets the message reappear for
// redelivery. Ack permanently removes a message; Nack makes it immediately
// visible again for another consumer.
type Queue interface {
	// Enqueue appends payload to namespace and returns the new message's
	// ID. Enqueue returns a cascade.KindQuotaExhausted error if namespace
	// has reached its capacity limit (enqueue-overflow) — the backend
	// itself is healthy, but the namespace has no free capacity, which
	// calls for backpressure rather than a bare retry (R-14.125: this is
	// distinct from KindUnavailable, which means the backend is
	// unreachable).
	Enqueue(ctx context.Context, namespace string, payload []byte) (id string, err error)

	// Dequeue claims the next visible message in namespace, making it
	// invisible to other Dequeue calls for visibilityTimeout. Dequeue
	// returns a nil Message and a nil error (not a taxonomy error) when
	// namespace currently has no visible message — an empty queue is
	// ordinary traffic, not a failure.
	Dequeue(ctx context.Context, namespace string, visibilityTimeout time.Duration) (*Message, error)

	// Ack permanently removes the message identified by receipt from
	// namespace. Ack returns a cascade.KindTimeout error if receipt's
	// visibility window already elapsed — the message has since been
	// redelivered under a new receipt (ack-timeout), and this receipt no
	// longer authorizes removal.
	Ack(ctx context.Context, namespace, receipt string) error

	// Nack releases the message identified by receipt back to namespace
	// immediately, making it visible for redelivery ahead of its
	// visibility timeout. Nack returns a cascade.KindTimeout error under
	// the same already-redelivered condition as Ack.
	Nack(ctx context.Context, namespace, receipt string) error
}

// Message is one queue entry returned by Queue.Dequeue.
type Message struct {
	// ID is the message's stable identifier, assigned at Enqueue and
	// unchanged across redeliveries.
	ID string
	// Payload is the enqueued content, unchanged since Enqueue.
	Payload []byte
	// Receipt is the opaque handle this specific delivery must present to
	// Ack or Nack. It is distinct from ID: a redelivered message keeps its
	// ID but receives a fresh Receipt, so a stale receipt from an earlier
	// delivery can be told apart from the current one (the ack-timeout
	// error path).
	Receipt string
}
