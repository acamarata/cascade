// Package events implements the daemon's typed, persistent pub/sub event
// bus: Publish appends a typed Event to a namespace's durable, strictly-
// ordered log; Subscribe tails that log from a named, durably-committed
// replay cursor; Replay re-reads a bounded historical range on demand.
// This is the runtime pub/sub backbone the scheduler, hooks, doctor,
// crash-safety, and memory consolidation are built on (C/S-04.T4,
// C/S-05.T2/T3, G/S-13.T4) — see the ticket's full_desc for the consumer
// list.
//
// # Persistence and domain placement
//
// R-14.5 closes the cascade.db domain set at ten members; events has no
// domain of its own. This ticket's contract names "B/S-02.T4 Queue
// domain" as the backing store, which this package reads as: persist
// through the "queue" domain (internal/storage/domains.go's DomainQueue,
// owned by internal/storage/queue) via the SAME provider.Store scoping
// convention every domain uses — a namespace string. It does NOT mean
// routing through provider.Queue's Enqueue/Dequeue/Ack/Nack surface:
// that interface's Dequeue CLAIMS a message for exactly one consumer
// (removing it from visibility for everyone else), which is fundamentally
// incompatible with this ticket's central requirement — multiple
// independent named cursors replaying the SAME events without stealing
// them from one another. Bus therefore depends on provider.Store
// directly, exactly as internal/storage/queue.Queue itself does, and
// callers wire it to the same domain-scoped Store internal/storage/queue
// uses (composition-root wiring, outside this ticket's scope — matching
// R-14.146's precedent for where that seam is authorized to live). This
// divergence from a literal reading of the ticket text is deliberate;
// see the ticket's completion report for the full rationale.
//
// # Ordering
//
// A namespace passed to Publish/Subscribe/Replay is this package's unit of
// ordering — an event log ("topic"), mirroring internal/storage/queue's
// own per-namespace design. Every event published to one namespace is
// assigned a strictly increasing, gapless Seq (1-based; see types.go) and
// every subscriber to that namespace observes every event in that exact
// Seq order. There is NO ordering guarantee across different namespaces.
//
// # Delivery guarantee and cursor semantics
//
// A cursor's persisted value is the LAST COMMITTED Seq — the highest Seq
// that has been fully handed to its subscriber's channel. commitCursor
// (cursor.go) is called ONLY after the channel send to the subscriber
// already succeeded (deliverLoop, bus_subscribe.go), so:
//
//   - A crash between the event landing in Store and the cursor commit
//     redelivers that event on the next Subscribe with the same name —
//     the cursor is still at its old value, so replayFrom(cursor) finds
//     the event again. This is what makes the bus AT-LEAST-ONCE, never
//     at-most-once: no committed advance can ever race ahead of an actual
//     delivery.
//   - A crash after the cursor commit never redelivers that event — from
//     the bus's perspective it was delivered, full stop. What the
//     subscriber's own application code did with it after receiving it
//     from the channel is outside the bus's guarantee (there is no
//     separate application-level Ack in this ticket's API — Subscribe's
//     channel receipt IS the commit point).
//
// Replay(offset) and the cursor-driven resume both read as EXCLUSIVE of
// offset: they return events with Seq > offset. This is the only
// consistent pairing with "cursor = last committed Seq" — resuming at
// cursor+1 never re-delivers the event the cursor already accounts for,
// and never skips one either.
//
// # Backpressure
//
// Publish never blocks on a subscriber — it only depends on the Store
// write succeeding. Each subscription has its own bounded channel
// (Subscribe's bufferSize) and its own background delivery goroutine that
// pulls from Store independently. A slow subscriber's delivery goroutine
// blocks on its OWN channel send until the subscriber drains it or the
// subscription is stopped (Unsubscribe/Close); memory is bounded by
// bufferSize, nothing is ever silently dropped, and a stalled subscriber
// never slows down Publish or any other subscription. A dead subscriber
// (never reads, never unsubscribes) leaks nothing beyond its own blocked
// goroutine, which Unsubscribe/Close always terminates deterministically —
// see bus_subscribe.go's deliverLoop and bus_backpressure_test.go.
package events

import (
	"context"
	"fmt"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// eventKeyPrefix namespaces persisted event records apart from cursor
// records (cursorKeyPrefix, cursor.go) within one Store namespace.
const eventKeyPrefix = "event:"

// eventSeqDigits is the zero-padded width of an event key's Seq suffix —
// wide enough that key lexical order equals numeric Seq order for every
// representable uint64 (max uint64 is 20 digits), which is what lets
// Store.Scan's documented "in key order" walk double as replay order.
const eventSeqDigits = 20

func eventKey(seq uint64) string {
	return fmt.Sprintf("%s%0*d", eventKeyPrefix, eventSeqDigits, seq)
}

// namespaceLog is one namespace's in-memory bookkeeping: the next Seq
// Publish will assign, every currently active subscription keyed by
// cursor name, and the "wake" broadcast channel deliverLoop's goroutines
// block on between polls (bus_subscribe.go's pull). None of this survives
// a process restart by construction — nextSeq is reconstructed from Store
// on first touch (recoverMaxSeq) and subs starts empty (a restarted
// process must call Subscribe again; its cursor resumes from Store,
// exactly as the durability contract promises).
type namespaceLog struct {
	nextSeq uint64
	subs    map[string]*subscription
	wake    chan struct{}
}

// Bus is the typed, persistent, multi-namespace pub/sub event log. The
// zero value is not usable; construct with New.
type Bus struct {
	store provider.Store
	clock runtime.Clock

	mu     sync.Mutex
	logs   map[string]*namespaceLog
	closed bool
}

// New returns a ready-to-use Bus persisting through store and stamping
// every Event's Timestamp from clock. Pass runtime.NewSystemClock() in
// production and a testkit.FrozenClock (or any structurally identical
// Clock) in tests — Bus never reads the wall clock itself (R-14.11).
func New(store provider.Store, clock runtime.Clock) *Bus {
	return &Bus{
		store: store,
		clock: clock,
		logs:  make(map[string]*namespaceLog),
	}
}

// Publish appends an Event of the given kind, source, and payload to
// namespace's log and returns the persisted Event (with its assigned Seq
// and Timestamp filled in). Publish returns a cascade.KindUnavailable
// error, never a swallowed one, if the underlying Store write fails or if
// the Bus has been Closed.
func (b *Bus) Publish(ctx context.Context, namespace string, kind EventKind, source string, payload []byte) (Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return Event{}, cascade.New(cascade.KindUnavailable, "events: Publish called after Close")
	}
	log, err := b.namespaceLogLocked(ctx, namespace)
	if err != nil {
		return Event{}, err
	}

	ev := Event{
		Seq:       log.nextSeq + 1,
		Kind:      kind,
		Source:    source,
		Payload:   append([]byte(nil), payload...),
		Timestamp: b.clock.Now(),
	}
	if err := b.store.Put(ctx, namespace, eventKey(ev.Seq), encodeEvent(ev)); err != nil {
		return Event{}, cascade.Wrapf(cascade.KindUnavailable, err, "events: publishing to namespace %q", namespace)
	}
	log.nextSeq = ev.Seq

	close(log.wake)
	log.wake = make(chan struct{})

	return ev.clone(), nil
}

// namespaceLogLocked returns (creating and recovering from Store if
// absent) namespace's in-memory log state. Caller MUST hold b.mu.
func (b *Bus) namespaceLogLocked(ctx context.Context, namespace string) (*namespaceLog, error) {
	log, ok := b.logs[namespace]
	if ok {
		return log, nil
	}
	maxSeq, err := recoverMaxSeq(ctx, b.store, namespace)
	if err != nil {
		return nil, err
	}
	log = &namespaceLog{
		nextSeq: maxSeq,
		subs:    make(map[string]*subscription),
		wake:    make(chan struct{}),
	}
	b.logs[namespace] = log
	return log, nil
}

// recoverMaxSeq reconstructs namespace's current Seq high-water mark by
// scanning its persisted event records — the "simulated restart" recovery
// path: a fresh Bus instance over a Store that already holds prior events
// (TestEventBusReplayCursor) must resume Publish numbering exactly where
// the previous instance left off, never re-using or gapping a Seq.
func recoverMaxSeq(ctx context.Context, store provider.Store, namespace string) (uint64, error) {
	it, err := store.Scan(ctx, namespace, eventKeyPrefix)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "events: recovering sequence for namespace %q", namespace)
	}
	defer func() { _ = it.Close() }()

	var maxSeq uint64
	for it.Next(ctx) {
		seq, perr := parseEventSeq(it.Key())
		if perr != nil {
			return 0, perr
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	if iterErr := it.Err(); iterErr != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, iterErr, "events: recovering sequence for namespace %q", namespace)
	}
	return maxSeq, nil
}
