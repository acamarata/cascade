// Package journal is the fleet sub-package's append-only entity event log
// (02-TARGET-STRUCTURE.md §internal/fleet; fleet surfaces share the
// census+journal data plane, 07-CLI-COMMAND-TREE.md §fleet). It gives
// session, task, and agent state a durable, checksummed, per-entity
// sequenced record that can be replayed to reconstruct state after a
// restart or a crash.
//
// Purpose: define Entry, the closed Kind enum, Cursor, and the
//
//	Store contract (Append/Checkpoint/Replay/Close) every concrete
//	store in this package implements.
//
// Inputs: caller-supplied entity ids, kinds, operation ids and JSON
//
//	payloads; an injected runtime.Clock (never a bare time.Now, R-14.11).
//
// Outputs: sealed Entry values, or a pkg/cascade taxonomy error.
// Constraints: R-21.216 is binding and normative over this package's
//
//	shape. The Kind enum is CLOSED for P1 at exactly eight members; the
//	zero value is invalid (fail closed). No panic paths in non-test code.
//
// SPORT: internal.fleet.journal.Store/ADDED (P1-E13-W3-S27-T1).
package journal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Kind identifies one member of this package's CLOSED, P1-frozen entry-kind
// enumeration (R-21.216). Declared as a defined type so a typo is a
// compile-time mismatch, never a silently-wrong integer threaded through
// Append.
type Kind uint8

// The eight ratified entry kinds, in R-21.216's declaration order. The zero
// value is deliberately not a member, so a forgotten Kind field reads as a
// bug rather than silently meaning KindIntent.
const (
	_ Kind = iota // 0 is deliberately not a valid Kind

	// KindIntent records that a caller is about to perform an externally
	// visible side effect. Fsynced before that side effect begins.
	KindIntent
	// KindAck records that the side effect an earlier KindIntent named has
	// completed. Fsynced after the side effect finishes.
	KindAck
	// KindCheckpoint records a published {entity_id, seq} cursor.
	KindCheckpoint
	// KindEscalation records an escalation-ladder event (S-27.T3's scope
	// reads these back with the kinds filter).
	KindEscalation
	// KindResumeCursor records a resumable-operation cursor distinct from
	// a journal Checkpoint.
	KindResumeCursor
	// KindFanOutLegStarted records that one leg of a fan-out operation
	// began.
	KindFanOutLegStarted
	// KindFanOutLegDone records that one leg of a fan-out operation
	// completed.
	KindFanOutLegDone
	// KindNodeStream records a node-streamed record.
	KindNodeStream
)

// kindNames holds the display string for each valid Kind, indexed by Kind
// value. Index 0 is the invalid zero value's placeholder.
var kindNames = [...]string{
	"",
	"intent",
	"ack",
	"checkpoint",
	"escalation",
	"resume-cursor",
	"fan-out-leg-started",
	"fan-out-leg-done",
	"node-stream",
}

// String returns the kind's stable lowercase-hyphenated name.
func (k Kind) String() string {
	if !k.Valid() {
		return "invalid-kind"
	}
	return kindNames[k]
}

// Valid reports whether k is a member of the closed eight-kind enumeration.
// The zero value and any value beyond KindNodeStream are invalid.
func (k Kind) Valid() bool {
	return k >= KindIntent && k <= KindNodeStream
}

// AllKinds returns the closed set of entry kinds in R-21.216's declaration
// order. Tests use it to assert the enumeration stays exactly eight members.
func AllKinds() []Kind {
	return []Kind{
		KindIntent, KindAck, KindCheckpoint, KindEscalation,
		KindResumeCursor, KindFanOutLegStarted, KindFanOutLegDone,
		KindNodeStream,
	}
}

// Entry is one sealed record in an entity's log. Every field is
// written once and never rewritten; there is no update path in this
// package, only Append, Checkpoint and Replay.
type Entry struct {
	// EntityID names the entity (task, session, agent) this entry
	// belongs to.
	EntityID string `json:"entity_id"`
	// Seq is the entry's 1-based position within EntityID's log,
	// allocated monotonically per entity inside the write-executor
	// transaction (R-21.216). Gapless: a caller that trusts the log
	// contents may treat two consecutive Seq numbers as adjacent.
	Seq uint64 `json:"seq"`
	// Kind is one of the eight ratified entry kinds.
	Kind Kind `json:"kind"`
	// OperationID is a stable, caller-supplied identifier for the
	// logical operation this entry describes. Replay is idempotent by
	// this field (see replay.go).
	OperationID string `json:"operation_id"`
	// Payload is the entry's caller-supplied body, decoded with the
	// standard library's encoding/json — no custom parser or wire
	// format, so this package is exempt from 06-FORGE-SPEC.md §5.7's
	// FuzzXxx requirement (same reasoning as Q/S-37.T2: "no new wire
	// format ⇒ no fuzz target").
	Payload json.RawMessage `json:"payload,omitempty"`
	// TSUnixNano is the append instant, read from the injected clock.
	TSUnixNano int64 `json:"ts_unix_nano"`
	// Checksum is a BLAKE3 digest over the canonical encoding of every
	// other field, verified on every read (entry.go).
	Checksum string `json:"checksum"`
}

// Time returns the instant this entry was appended.
func (e Entry) Time() time.Time { return time.Unix(0, e.TSUnixNano).UTC() }

// Cursor names a position in one entity's log: the entity and the sequence
// number the cursor has advanced through (inclusive of what has already
// been processed). The zero value (empty EntityID, Seq 0) is the "absent"
// cursor Replay's fallback path recognizes.
type Cursor struct {
	EntityID string `json:"entity_id"`
	Seq      uint64 `json:"seq"`
}

// TruncationReport describes the torn-tail recovery SQLiteStore performs the
// first time it touches an entity's log: how many trailing entries were
// truncated because their checksum did not verify, and the first sequence
// number that failed. A report with Truncated == 0 means the log's tail
// was already fully consistent.
type TruncationReport struct {
	Truncated   int
	FirstBadSeq uint64
}

// Store is the contract every concrete entity journal implements.
type Store interface {
	// Append seals a new entry as the next sequence number in entityID's
	// log and commits it durably before returning.
	Append(ctx context.Context, entityID string, kind Kind, operationID string, payload json.RawMessage) (Entry, error)

	// Checkpoint atomically publishes cursor together with the sequence
	// it covers. Re-checkpointing the same cursor is a no-op.
	Checkpoint(ctx context.Context, cursor Cursor) error

	// Replay returns entityID's entries with Seq > cursor.Seq (or, when
	// cursor is absent or names a different entity, from the last
	// persisted checkpoint), filtered by kinds (empty means all kinds),
	// deduplicated so a retried (kind, operation_id) pair is returned
	// only once (an Intent and its Ack share an operation_id and are
	// never deduplicated against each other, since they differ in kind).
	Replay(ctx context.Context, entityID string, cursor Cursor, kinds []Kind) ([]Entry, error)

	// Close releases the store's resources.
	Close() error
}

// Compile-time proof that SQLiteStore satisfies the contract it implements.
var _ Store = (*SQLiteStore)(nil)

// Sentinel errors. Each wraps exactly one frozen pkg/cascade.Kind (R-14.2):
// domain-specific sentinels live in their owning package, never in
// pkg/cascade itself.
var (
	// ErrUnknownKind is returned for an entry kind outside the closed
	// eight, whether supplied by a caller or decoded from storage.
	ErrUnknownKind = cascade.New(cascade.KindInvalidInput, "journal: unknown entry kind")
	// ErrInvalidEntry is returned for an otherwise malformed entry or
	// call (empty entity id or operation id).
	ErrInvalidEntry = cascade.New(cascade.KindInvalidInput, "journal: invalid entry")
	// ErrTampered is returned when a stored entry's checksum does not
	// match its content, or its shape cannot be decoded at all.
	ErrTampered = cascade.New(cascade.KindIntegrity, "journal: entry checksum verification failed")
	// ErrAlreadyRecorded is returned when an append would land on a
	// sequence number that already holds an entry for that entity.
	ErrAlreadyRecorded = cascade.New(cascade.KindConflict, "journal: sequence already recorded for entity")
	// ErrCheckpointBeyondLog is returned when a caller asks to checkpoint
	// a sequence number the log has not actually reached, which would let
	// a reader observe a cursor claiming coverage the log does not have.
	ErrCheckpointBeyondLog = cascade.New(cascade.KindConflict, "journal: checkpoint sequence exceeds entity's log")
	// ErrStoreUnavailable is returned when the backing store fails for a
	// reason this package did not itself raise.
	ErrStoreUnavailable = cascade.New(cascade.KindUnavailable, "journal: store unavailable")
)

// wrapStore classifies a store failure. A refusal this package raised
// itself (a conditional-create conflict, an integrity alarm, invalid
// input) keeps its own Kind; anything else from the driver becomes
// ErrStoreUnavailable.
func wrapStore(err error, what string) error {
	for _, k := range []cascade.Kind{cascade.KindConflict, cascade.KindIntegrity, cascade.KindInvalidInput} {
		if cascade.HasKind(err, k) {
			return err
		}
	}
	return cascade.Wrapf(cascade.KindUnavailable, ErrStoreUnavailable, "%s: %v", what, err)
}
