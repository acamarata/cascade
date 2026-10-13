package audit

// Purpose: the append-only writer. Log.Append seals one Record into the
//   audit domain namespace through pkg/provider.Store, links it to the
//   record before it by hash, and emits a typed event-bus notification.
//   There is no update path and no delete path in this package at all:
//   the type's whole method set is Append, Query, Explain and Verify.
// Inputs: a provider.Store (the SQLite driver routes every write through
//   the single write-connection executor and its per-domain fairness
//   queue), an injected runtime.Clock, and an optional Publisher.
// Outputs: the sealed Record, or a pkg/cascade taxonomy error.
// Constraints: no bare time.Now (R-14.11); crypto/rand only; the write is
//   a conditional create and a compare-and-swap of the head pointer, so a
//   second writer racing for the same sequence number is refused instead
//   of overwriting a record that already exists. A writer that lost the
//   race re-reads the head and tries again (three retries), so several
//   Log values over one store never fork the chain.
// SPORT: internal.audit.Log/ADDED (P1-E09-W2-S18-T2).

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EventKindRecorded is the event-bus kind published once a record is
// committed. Its payload names the record and its kind, never the record's
// body: a subscriber that wants the record reads it back through Explain,
// under the same integrity checks every other reader gets.
const EventKindRecorded events.EventKind = "audit.recorded"

// busSource identifies this package as the publisher on the bus.
const busSource = "internal/audit"

// Writer is the injection seam. Policy, the approval queue, config reload,
// and the elevation middleware take a Writer rather than a *Log, so none of
// them imports this package's implementation and the dependency direction
// stays one-way: subsystem to audit, never back.
type Writer interface {
	// Append seals event into the log and returns the committed record.
	Append(ctx context.Context, event Event) (Record, error)
}

// Compile-time proof that the concrete log satisfies the seam its
// callers inject. Without this the interface and the implementation could
// drift apart silently, and every caller would find out at wiring time.
var _ Writer = (*Log)(nil)

// Publisher is the event-bus seam, satisfied by *events.Bus. It is declared
// here rather than imported as a concrete type so a caller can wire a
// different bus, and so a Log with no bus at all is a legal, documented
// configuration rather than a nil-pointer hazard.
type Publisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind,
		source string, payload []byte) (events.Event, error)
}

// head is the log's tail pointer: the sequence number and hash of the most
// recent record. It is what makes truncation of the newest records
// detectable, a scan that reaches the end of the log and finds a last
// record that does not match head has lost records off the end.
type head struct {
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
}

// Log is the append-only audit log. The zero value is not usable;
// construct with New.
type Log struct {
	store provider.Store
	clock runtime.Clock
	bus   Publisher
	// redactor is the secrets substitution seam every appended record's
	// free-text fields pass through. See redact.go; a nil redactor is a
	// documented, unmounted configuration, not an error.
	redactor Redactor

	mu sync.Mutex
	// head and headRaw are the tail pointer as the last read of the store
	// saw it (headRaw is its exact stored bytes, nil when the store has
	// none yet). commitLocked swaps the stored head from headRaw, so a
	// head another writer moved is a conflict, never an overwrite.
	head    head
	headRaw []byte
}

// appendAttempts is the first try plus three retries after a conflict.
const appendAttempts = 4

// New returns a Log persisting through store and stamping every record
// from clock. Pass runtime.NewSystemClock() in production and a frozen
// clock in tests. A nil bus disables notification; every other behaviour is
// unchanged.
func New(store provider.Store, clock runtime.Clock, bus Publisher) *Log {
	return &Log{store: store, clock: clock, bus: bus}
}

// Append seals event as the next record in the log and commits it.
//
// Ordering is by the log's own sequence number, not by the timestamp and
// not by the record id, so two records appended within one clock tick still
// have exactly one defined order and both are readable.
//
// On a successful store commit but a failed bus notification, Append
// returns the committed record together with a non-nil error. The record IS
// in the log; retrying would append a second copy of the same event. The
// error says so.
func (l *Log) Append(ctx context.Context, event Event) (Record, error) {
	if err := refuseEffectFields(event); err != nil {
		return Record{}, err
	}
	if err := l.redactEvent(ctx, &event); err != nil {
		return Record{}, err
	}
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, err := l.sealAndCommitLocked(ctx, event)
	if err != nil {
		return Record{}, err
	}
	return rec, l.notify(ctx, rec)
}

// sealAndCommitLocked reads the head, seals event as the next record and
// commits it. A conflict (another writer took the sequence number or moved
// the head) re-reads the head and seals again, up to appendAttempts times;
// the last conflict is returned as KindConflict wrapping ErrAlreadyRecorded
// with nothing written.
func (l *Log) sealAndCommitLocked(ctx context.Context, event Event) (Record, error) {
	if l.store == nil {
		return Record{}, cascade.Wrapf(cascade.KindUnavailable, ErrStoreUnavailable, "this log has no store")
	}
	for attempt := 1; ; attempt++ {
		if err := l.loadHeadLocked(ctx); err != nil {
			return Record{}, err
		}
		rec, err := l.buildLocked(event)
		if err != nil {
			return Record{}, err
		}
		err = l.commitLocked(ctx, rec)
		if err == nil {
			l.head = head{Seq: rec.Seq, Hash: rec.Hash}
			return rec, nil
		}
		if !cascade.HasKind(err, cascade.KindConflict) || attempt >= appendAttempts {
			return Record{}, err
		}
		if perr := retryPause(ctx, attempt); perr != nil {
			return Record{}, perr
		}
	}
}

// retryPause waits a random fraction of 2^attempt milliseconds before a
// retry. Two writers retrying in lockstep would keep losing to each other
// (the one that read second always commits second); the random gap breaks
// the pattern. crypto/rand only; a cancelled context ends the wait.
func retryPause(ctx context.Context, attempt int) error {
	var b [1]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "audit: retry jitter")
	}
	timer := time.NewTimer(time.Duration(b[0]) * (time.Millisecond << attempt) / 256)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return cascade.Wrap(cascade.KindUnavailable, ctx.Err(), "audit: append abandoned while waiting to retry")
	}
}

// buildLocked mints the next record's id, time, position and chain link.
func (l *Log) buildLocked(event Event) (Record, error) {
	now := l.clock.Now().UTC()
	id, err := newID(now)
	if err != nil {
		return Record{}, err
	}
	return seal(Record{
		Seq:        l.head.Seq + 1,
		ID:         id,
		TSUnixNano: now.UnixNano(),
		Event:      event,
		PrevHash:   l.head.Hash,
	})
}

// commitLocked writes the record, its id index, and the new head pointer in
// one transaction. The head moves with CompareAndSwap from the exact bytes
// this Log read, so a writer holding a stale tail is refused. The record
// itself goes in with CompareAndSwap against a nil prior value, which is a
// conditional create: if anything already occupies that sequence number the
// write is refused instead of replacing it. That refusal is the append-only
// guarantee at the storage layer, not merely the absence of an update
// method on this type.
func (l *Log) commitLocked(ctx context.Context, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "audit: encoding record")
	}
	headData, err := json.Marshal(head{Seq: rec.Seq, Hash: rec.Hash})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "audit: encoding head")
	}
	txErr := l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := tx.CompareAndSwap(ctx, namespace, headKey, l.headRaw, headData); err != nil {
			return conflictOrRaw(err, "head pointer moved before sequence %d: %v", rec.Seq)
		}
		if err := tx.CompareAndSwap(ctx, namespace, recordKey(rec.Seq), nil, data); err != nil {
			return conflictOrRaw(err, "sequence %d: %v", rec.Seq)
		}
		return tx.Put(ctx, namespace, indexKey(rec.ID), []byte(strconv.FormatUint(rec.Seq, 10)))
	})
	if txErr != nil {
		return wrapStore(txErr, "appending record")
	}
	l.headRaw = headData
	return nil
}

// conflictOrRaw turns a refused compare-and-swap into KindConflict wrapping
// ErrAlreadyRecorded, and leaves any other store failure as it came.
func conflictOrRaw(err error, format string, seq uint64) error {
	if !cascade.HasKind(err, cascade.KindConflict) {
		return err
	}
	return cascade.Wrapf(cascade.KindConflict, ErrAlreadyRecorded, format, seq, err)
}

// notify publishes the committed record's identity on the bus.
func (l *Log) notify(ctx context.Context, rec Record) error {
	if l.bus == nil {
		return nil
	}
	payload, err := json.Marshal(struct {
		Seq  uint64 `json:"seq"`
		ID   string `json:"id"`
		Kind Kind   `json:"kind"`
	}{Seq: rec.Seq, ID: rec.ID, Kind: rec.Kind})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "audit: encoding notification")
	}
	if _, err := l.bus.Publish(ctx, namespace, EventKindRecorded, busSource, payload); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"audit: record %s is committed but its bus notification failed; do not retry the append", rec.ID)
	}
	return nil
}

// loadHeadLocked reads the tail pointer from the store, on every append,
// so a Log over a store another writer has moved numbers its next record
// from the stored head rather than from a stale copy. The exact stored
// bytes are kept for the compare-and-swap in commitLocked.
func (l *Log) loadHeadLocked(ctx context.Context) error {
	data, err := l.store.Get(ctx, namespace, headKey)
	if err != nil {
		if !cascade.HasKind(err, cascade.KindNotFound) {
			return wrapStore(err, "reading head pointer")
		}
		l.head, l.headRaw = head{}, nil
		return nil
	}
	var h head
	if uerr := json.Unmarshal(data, &h); uerr != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered, "head pointer is not decodable: %v", uerr)
	}
	l.head, l.headRaw = h, append([]byte(nil), data...)
	return nil
}

// wrapStore classifies a store failure. A refusal this package raised
// itself (a conditional-create conflict, an integrity alarm) keeps its own
// Kind; anything else from the driver becomes ErrStoreUnavailable.
func wrapStore(err error, what string) error {
	for _, k := range []cascade.Kind{cascade.KindConflict, cascade.KindIntegrity, cascade.KindInvalidInput} {
		if cascade.HasKind(err, k) {
			return err
		}
	}
	return cascade.Wrapf(cascade.KindUnavailable, ErrStoreUnavailable, "%s: %v", what, err)
}
