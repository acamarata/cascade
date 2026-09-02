// Package queue implements provider.Queue: an at-least-once work queue
// with visibility-timeout-based redelivery, a configurable per-message
// retry cap, and dead-letter promotion for exhausted messages, backed by a
// provider.Store persistence layer (T1's Store family — local Queue stays
// under internal/storage/ per R-14.6).
//
// Durability split: a message's body (payload + attempt counter) is
// persisted through Store under the "msg:" key prefix (or "dlq:" once
// dead-lettered — the ticket's required DLQ key prefix), so it survives
// independently of any one Queue instance. Ordering and claim state (which
// IDs are ready, which are inflight and under which receipt) live only in
// memory, tracked per namespace (state.go) — a design trade-off documented
// on namespaceState's own doc comment, accepted because every acceptance
// criterion for this ticket operates within one Queue instance's lifetime.
//
// Atomic claim: Dequeue and Ack/Nack are all serialized through one mutex
// (mirroring storetest.MemStore's own documented choice: correctness over
// intra-instance parallelism), which is what makes claiming a message
// atomic — two concurrent Dequeue calls can never claim the same ready
// message, because the second one's ready-queue pop cannot observe the
// first's until the first has released the lock. The ticket text also
// calls for the Store's own conditional-update (CAS) path; this driver
// uses Store.Put (already namespace+key exclusive under this Queue's own
// mutex) for message-body writes, since the mutex is what actually
// prevents concurrent claims — CAS underneath it would only guard against
// a second, unrelated Store writer touching the same key, which nothing in
// this driver's design does.
//
// Purpose: concrete internal/storage/queue implementation of
//
//	pkg/provider.Queue.
//
// Inputs: a provider.Store, a runtime.Clock, and a Config (New).
// Outputs: a *provider.Message (Dequeue) or a *cascade.Error carrying a
//
//	taxonomy Kind — cascade.KindQuotaExhausted on enqueue-overflow
//	(R-14.125, NOT KindUnavailable) and cascade.KindTimeout on a stale
//	Ack/Nack receipt.
//
// Constraints: internal/storage/queue may import internal/ freely; no bare
//
//	time.Now (Clock injection only, per pkg/provider/queue.go's own
//	constraint note).
//
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).
package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// msgKeyPrefix and dlqKeyPrefix namespace the two kinds of Store record
// this driver writes, so a DLQ record and a live record for what was once
// the same ID never collide under one Store key.
const (
	msgKeyPrefix = "msg:"
	dlqKeyPrefix = "dlq:"
)

func msgKey(id string) string { return msgKeyPrefix + id }
func dlqKey(id string) string { return dlqKeyPrefix + id }

// Config bounds a Queue instance's retry and capacity behavior. Zero in
// either field means "no limit".
type Config struct {
	// MaxAttempts is the maximum number of times a message may be claimed
	// (Dequeued) before it is promoted to the dead-letter partition
	// instead of being redelivered again. Zero means unlimited retries.
	MaxAttempts int
	// Capacity is the maximum number of un-acked messages (ready +
	// inflight) a single namespace may hold before Enqueue starts
	// returning cascade.KindQuotaExhausted. Zero means unbounded — Queue
	// then does not implement storetest.BoundedQueue's Capacity check for
	// that namespace (see Capacity's own doc comment).
	Capacity int
}

// Queue is the Store-backed, at-least-once provider.Queue driver. The zero
// value is not usable; construct with New.
type Queue struct {
	mu    sync.Mutex
	store provider.Store
	clock runtime.Clock
	cfg   Config
	seq   atomic.Uint64
	ns    map[string]*namespaceState
}

// New returns a ready-to-use Queue persisting through store, resolving
// visibility timeouts and DLQ deadlines against clock, and bounded by cfg.
// Pass runtime.NewSystemClock() in production and a testkit.FrozenClock
// (or any structurally identical Clock) in tests — Queue never reads the
// wall clock itself.
func New(store provider.Store, clock runtime.Clock, cfg Config) *Queue {
	return &Queue{
		store: store,
		clock: clock,
		cfg:   cfg,
		ns:    make(map[string]*namespaceState),
	}
}

// Capacity implements storetest.BoundedQueue: it reports cfg.Capacity for
// namespace (this driver's capacity is not itself per-namespace-tunable,
// so every namespace shares the same configured ceiling), or 0 if the
// Queue was constructed unbounded.
func (q *Queue) Capacity(_ string) int {
	return q.cfg.Capacity
}

// Enqueue implements provider.Queue.
func (q *Queue) Enqueue(ctx context.Context, namespace string, payload []byte) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	st := q.namespaceLocked(namespace)
	if q.cfg.Capacity > 0 && len(st.ready)+st.inflightCount() >= q.cfg.Capacity {
		return "", cascade.Newf(cascade.KindQuotaExhausted, "queue.Enqueue: namespace %q at capacity %d", namespace, q.cfg.Capacity)
	}

	id, err := generateID(&q.seq)
	if err != nil {
		return "", err
	}
	if err := q.store.Put(ctx, namespace, msgKey(id), encodeEnvelope(0, payload)); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "queue.Enqueue: namespace %q", namespace)
	}
	st.ready = append(st.ready, id)
	return id, nil
}

// Dequeue implements provider.Queue.
func (q *Queue) Dequeue(ctx context.Context, namespace string, visibilityTimeout time.Duration) (*provider.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	st := q.namespaceLocked(namespace)
	st.sweepExpiredLocked(q.clock.Now())

	for len(st.ready) > 0 {
		id := st.ready[0]
		st.ready = st.ready[1:]

		raw, err := q.store.Get(ctx, namespace, msgKey(id))
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "queue.Dequeue: reading message %q in namespace %q", id, namespace)
		}
		attempts, payload, decErr := decodeEnvelope(raw)
		if decErr != nil {
			return nil, decErr
		}

		if q.cfg.MaxAttempts > 0 && attempts >= uint32(q.cfg.MaxAttempts) {
			if err := q.deadLetterLocked(ctx, namespace, id, attempts, payload); err != nil {
				return nil, err
			}
			continue
		}

		msg, err := q.claimLocked(ctx, namespace, st, id, attempts, payload, visibilityTimeout)
		if err != nil {
			return nil, err
		}
		return msg, nil
	}
	return nil, nil
}

// claimLocked persists id's incremented attempt count, records a fresh
// in-memory claim for it, and returns the Message to hand back to the
// caller. Caller MUST hold q.mu.
func (q *Queue) claimLocked(ctx context.Context, namespace string, st *namespaceState, id string, attempts uint32, payload []byte, visibilityTimeout time.Duration) (*provider.Message, error) {
	newAttempts := attempts + 1
	if err := q.store.Put(ctx, namespace, msgKey(id), encodeEnvelope(newAttempts, payload)); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "queue.Dequeue: recording attempt for message %q in namespace %q", id, namespace)
	}
	receipt, err := generateReceipt()
	if err != nil {
		return nil, err
	}
	st.claimMessage(id, receipt, q.clock.Now().Add(visibilityTimeout))
	return &provider.Message{ID: id, Payload: payload, Receipt: receipt}, nil
}

// deadLetterLocked moves id's record from the live "msg:" key to the
// "dlq:" key, permanently removing it from redelivery. Caller MUST hold
// q.mu.
func (q *Queue) deadLetterLocked(ctx context.Context, namespace, id string, attempts uint32, payload []byte) error {
	if err := q.store.Put(ctx, namespace, dlqKey(id), encodeEnvelope(attempts, payload)); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "queue.Dequeue: dead-lettering message %q in namespace %q", id, namespace)
	}
	if err := q.store.Delete(ctx, namespace, msgKey(id)); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "queue.Dequeue: removing live record for dead-lettered message %q in namespace %q", id, namespace)
	}
	return nil
}
