// Package queue implements provider.Queue: an at-least-once work queue
// with visibility-timeout-based redelivery, a configurable per-message
// retry cap, and dead-letter promotion for exhausted messages, backed by a
// provider.Store persistence layer (T1's Store family — local Queue stays
// under internal/storage/ per R-14.6).
//
// Durability: every field that decides a message's
// fate — ready/claimed state, attempt count, receipt and visibility
// deadline, not merely its payload — is ONE Store value per message
// (persistence.go's record), and every transition (claim, Ack, Nack, DLQ
// move) is exactly one Store.Tx whose writes are CompareAndSwap-fenced on
// the record's prior bytes (a cascade.KindConflict aborts the Tx and the
// caller retries or moves on; no transition spans two Tx calls).
// A new Queue instance reconstructs ready/inflight ordering for a
// namespace by scanning Store on that namespace's first touch
// (state.go's namespaceLocked -> persistence.go's recoverNamespace), so
// restart, an interrupted transition, and two concurrent instances over
// one Store all behave per the acceptance criteria below.
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
// SPORT: internal.storage.queue.Queue/CHANGED.
package queue

import (
	"bytes"
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
// wall clock itself. New performs no I/O; a namespace's persisted state is
// recovered from store lazily, the first time that namespace is touched.
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

	st, err := q.namespaceLocked(ctx, namespace)
	if err != nil {
		return "", err
	}
	if q.cfg.Capacity > 0 && len(st.ready)+len(st.receipts) >= q.cfg.Capacity {
		return "", cascade.Newf(cascade.KindQuotaExhausted, "queue.Enqueue: namespace %q at capacity %d", namespace, q.cfg.Capacity)
	}

	id, err := generateID(&q.seq)
	if err != nil {
		return "", err
	}
	newBytes := encodeRecord(record{payload: payload})
	if err := q.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, namespace, msgKey(id), nil, newBytes)
	}); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "queue.Enqueue: namespace %q", namespace)
	}
	st.records[id] = &trackedRecord{knownBytes: newBytes, payload: payload}
	st.ready = append(st.ready, id)
	return id, nil
}

// Dequeue implements provider.Queue.
func (q *Queue) Dequeue(ctx context.Context, namespace string, visibilityTimeout time.Duration) (*provider.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	st, err := q.namespaceLocked(ctx, namespace)
	if err != nil {
		return nil, err
	}
	st.sweepExpiredLocked(q.clock.Now())

	for len(st.ready) > 0 {
		id := st.ready[0]
		st.ready = st.ready[1:]
		tr := st.records[id]

		if q.cfg.MaxAttempts > 0 && tr.attempts >= uint32(q.cfg.MaxAttempts) {
			_, dlqErr := q.deadLetterLocked(ctx, namespace, id, tr)
			if dlqErr != nil {
				st.ready = append([]string{id}, st.ready...) // Tx rolled back: still live, still reachable
				return nil, dlqErr
			}
			delete(st.records, id) // won: dead-lettered; lost: another instance already moved it
			continue
		}

		msg, won, claimErr := q.claimLocked(ctx, namespace, st, id, tr, visibilityTimeout)
		if claimErr != nil {
			st.ready = append([]string{id}, st.ready...)
			return nil, claimErr
		}
		if !won {
			delete(st.records, id) // lost the CAS race: another instance holds it now
			continue
		}
		return msg, nil
	}
	return nil, nil
}

// claimLocked attempts to claim id via ONE Store.Tx, CAS-fenced on tr's
// prior known bytes (no transition spans two Tx calls). won is
// false (with a nil error) when the CAS lost to a concurrent claimant —
// id is no longer this Queue instance's to track, not a failure. The
// receipt is generated before the Tx opens and committed only if the Tx
// succeeds, per the "generate a receipt before committing its
// claim" step. Caller MUST hold q.mu.
func (q *Queue) claimLocked(ctx context.Context, namespace string, st *namespaceState, id string, tr *trackedRecord, visibilityTimeout time.Duration) (msg *provider.Message, won bool, err error) {
	receipt, err := generateReceipt()
	if err != nil {
		return nil, false, err
	}
	newRec := record{claimed: true, attempts: tr.attempts + 1, receipt: receipt, deadline: q.clock.Now().Add(visibilityTimeout), payload: tr.payload}
	newBytes := encodeRecord(newRec)

	txErr := q.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, namespace, msgKey(id), tr.knownBytes, newBytes)
	})
	switch {
	case txErr == nil:
		tr.knownBytes, tr.attempts, tr.claimed, tr.receipt, tr.deadline = newBytes, newRec.attempts, true, receipt, newRec.deadline
		st.receipts[receipt] = id
		return &provider.Message{ID: id, Payload: tr.payload, Receipt: receipt}, true, nil
	case cascade.HasKind(txErr, cascade.KindConflict):
		return nil, false, nil
	default:
		return nil, false, cascade.Wrapf(cascade.KindUnavailable, txErr, "queue.Dequeue: claiming message %q in namespace %q", id, namespace)
	}
}

// deadLetterLocked moves id from its live "msg:" key to "dlq:" in ONE
// Store.Tx: the dead-letter record is CAS-created (old=nil) and the live
// record is removed via the delete-fencing pattern (tx.Get, a
// byte comparison against tr's prior known bytes, then tx.Delete — Tx has
// no conditional delete). won is false (nil error) when the fencing
// comparison lost to a concurrent transition — id was already claimed or
// dead-lettered elsewhere, not a failure here. Caller MUST hold q.mu.
func (q *Queue) deadLetterLocked(ctx context.Context, namespace, id string, tr *trackedRecord) (won bool, err error) {
	dlqBytes := encodeRecord(record{attempts: tr.attempts, payload: tr.payload})
	lostRace := false
	txErr := q.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := tx.CompareAndSwap(ctx, namespace, dlqKey(id), nil, dlqBytes); err != nil {
			return err
		}
		cur, getErr := tx.Get(ctx, namespace, msgKey(id))
		switch {
		case getErr != nil && cascade.HasKind(getErr, cascade.KindNotFound):
			lostRace = true
			return cascade.Newf(cascade.KindConflict, "queue: dead-lettering %q in namespace %q: live record already gone", id, namespace)
		case getErr != nil:
			return getErr
		case !bytes.Equal(cur, tr.knownBytes):
			lostRace = true
			return cascade.Newf(cascade.KindConflict, "queue: dead-lettering %q in namespace %q: live record changed", id, namespace)
		}
		return tx.Delete(ctx, namespace, msgKey(id))
	})
	switch {
	case txErr == nil:
		return true, nil
	case lostRace:
		return false, nil
	default:
		return false, cascade.Wrapf(cascade.KindUnavailable, txErr, "queue.Dequeue: dead-lettering message %q in namespace %q", id, namespace)
	}
}
