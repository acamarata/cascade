// Purpose: named replay cursor persistence — the durable "where did this
//
//	consumer get to" record that survives a process restart, kill -9, or
//	any other loss of Bus in-memory state (task 3: "open-or-create at Seq
//	0"; cursor advance is committed only after delivery — see bus_subscribe.go's
//	deliverLoop, the one caller of commitCursor).
//
// Inputs: a provider.Store, the namespace (Store-scoping argument, doubling
//
//	as the event-log/topic identity — see bus.go's package doc), and a
//	caller-chosen cursor name.
//
// Outputs: loadCursor returns 0 (never an error) for a name that has never
//
//	committed — "open-or-create at Seq 0" is a read-time default, not a
//	write; commitCursor persists a *cascade.Error-wrapped failure so a
//	caller (deliverLoop) can decide how to react rather than silently
//	losing the advance.
//
// Constraints: cursor semantics are CURSOR = LAST COMMITTED SEQ
//
//	(inclusive of what has already been delivered). Replay/Subscribe
//	therefore resume at cursor+1 — see bus_replay.go's replayFrom, which
//	is EXCLUSIVE of the offset passed to it. This is the single source of
//	truth for that semantic; see the package doc in bus.go for the full
//	at-least-once argument.
//
//	Stream cursors follow the per-stream sequence: streamCursors holds
//	each stream's high-water StreamSeq, recoverLog rebuilds it from the
//	persisted records, and ReplayStream resumes EXCLUSIVE of a StreamSeq.
//
// SPORT: internal.events.Bus/ADDED (cursor persistence) (P1-E03-W1-S04-T3).

package events

import (
	"context"
	"encoding/binary"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// cursorKeyPrefix namespaces cursor records apart from event records
// (eventKeyPrefix, bus.go) within the same Store namespace.
const cursorKeyPrefix = "cursor:"

// cursorValueSize is the fixed width of a persisted cursor record: one
// big-endian uint64 Seq.
const cursorValueSize = 8

func cursorKey(name string) string { return cursorKeyPrefix + name }

// loadCursor returns name's last-committed Seq in namespace. A name that
// has never been committed reads as 0 ("before the first event") with a
// nil error — the "open-or-create at Seq 0" contract from task 3; there is
// no separate create step because a cursor record is written lazily, the
// first time commitCursor is called for that name.
func loadCursor(ctx context.Context, store provider.Store, namespace, name string) (uint64, error) {
	raw, err := store.Get(ctx, namespace, cursorKey(name))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return 0, nil
		}
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "events: loading cursor %q in namespace %q", name, namespace)
	}
	if len(raw) != cursorValueSize {
		return 0, cascade.Newf(cascade.KindIntegrity,
			"events: cursor %q in namespace %q has malformed value (%d byte(s), want %d)", name, namespace, len(raw), cursorValueSize)
	}
	return binary.BigEndian.Uint64(raw), nil
}

// commitCursor durably advances name's cursor to seq. Called ONLY after an
// event has already been handed to its subscriber (deliverLoop, the sole
// caller) — never speculatively before delivery, and never for an event
// the subscriber did not actually receive. See bus.go's package doc for
// why this ordering is what makes the bus's delivery guarantee
// at-least-once rather than at-most-once.
func commitCursor(ctx context.Context, store provider.Store, namespace, name string, seq uint64) error {
	var buf [cursorValueSize]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	if err := store.Put(ctx, namespace, cursorKey(name), buf[:]); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "events: committing cursor %q in namespace %q to seq %d", name, namespace, seq)
	}
	return nil
}

// streamCursors maps a StreamID to the last StreamSeq durably persisted on
// it within one namespace. The Bus is the only allocator: Publish reads
// next under b.mu and commits only after the Store write succeeded, so a
// failed write never consumes a sequence and the sequence stays gapless.
type streamCursors map[string]uint64

// next returns the StreamSeq the next event on stream receives.
func (c streamCursors) next(stream string) uint64 { return c[stream] + 1 }

// commit records seq as stream's high-water mark.
func (c streamCursors) commit(stream string, seq uint64) {
	if seq > c[stream] {
		c[stream] = seq
	}
}

// normalizeStream places a legacy record (written before the stream
// extension, so with no StreamID) on namespace's default stream at
// StreamSeq equal to its Seq: every legacy event was published on the
// namespace's single ordered log.
func normalizeStream(ev *Event, namespace string) {
	if ev.StreamID == "" {
		ev.StreamID = BusStreamID(namespace)
		ev.StreamSeq = ev.Seq
	}
}

// recoverLog reconstructs namespace's Seq high-water mark and every
// stream's StreamSeq high-water mark by scanning its persisted event
// records — the "simulated restart" recovery path: a fresh Bus over a
// Store that already holds prior events resumes both numberings exactly
// where the previous instance left off, never re-using or gapping one.
func recoverLog(ctx context.Context, store provider.Store, namespace string) (uint64, streamCursors, error) {
	it, err := store.Scan(ctx, namespace, eventKeyPrefix)
	if err != nil {
		return 0, nil, cascade.Wrapf(cascade.KindUnavailable, err, "events: recovering sequence for namespace %q", namespace)
	}
	defer func() { _ = it.Close() }()

	var maxSeq uint64
	streams := make(streamCursors)
	for it.Next(ctx) {
		seq, perr := parseEventSeq(it.Key())
		if perr != nil {
			return 0, nil, perr
		}
		raw, derr := decodeEvent(it.Value())
		if derr != nil {
			return 0, nil, derr
		}
		ev := raw.clone()
		normalizeStream(&ev, namespace)
		if seq > maxSeq {
			maxSeq = seq
		}
		streams.commit(ev.StreamID, ev.StreamSeq)
	}
	if iterErr := it.Err(); iterErr != nil {
		return 0, nil, cascade.Wrapf(cascade.KindUnavailable, iterErr, "events: recovering sequence for namespace %q", namespace)
	}
	return maxSeq, streams, nil
}

// StreamHead returns the last StreamSeq allocated on stream in namespace
// (0 when the stream has no events), recovering it from Store on first
// touch. An empty stream reads namespace's default stream.
func (b *Bus) StreamHead(ctx context.Context, namespace, stream string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, cascade.New(cascade.KindUnavailable, "events: StreamHead called after Close")
	}
	log, err := b.namespaceLogLocked(ctx, namespace)
	if err != nil {
		return 0, err
	}
	if stream == "" {
		stream = BusStreamID(namespace)
	}
	return log.streams[stream], nil
}

// ReplayStream returns every event on stream in namespace with
// StreamSeq > after, in strictly increasing StreamSeq order, as of the
// call. A stream cursor is the last StreamSeq a consumer handled, so
// resuming at it neither re-delivers nor skips an event. An empty stream
// reads namespace's default stream; legacy records join it at their Seq.
func (b *Bus) ReplayStream(ctx context.Context, namespace, stream string, after uint64) ([]Event, error) {
	if stream == "" {
		stream = BusStreamID(namespace)
	}
	all, err := replayFrom(ctx, b.store, namespace, 0)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, ev := range all {
		normalizeStream(&ev, namespace)
		if ev.StreamID == stream && ev.StreamSeq > after {
			out = append(out, ev)
		}
	}
	return out, nil
}
