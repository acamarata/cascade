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
