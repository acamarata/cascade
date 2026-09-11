package conversation

// Purpose (this file): wires the conversation domain into the M/S-27.T1
//   entity journal (internal/fleet/journal): a TurnAppend journal entry
//   is written BEFORE each store commit (AppendTurnJournaled), a
//   C/S-04.T4-compatible checkpoint job (checkpointJob), and journal
//   replay/resume (Resume) that reconstructs store state from the
//   journal and is idempotent (a second Resume over the same journal
//   changes nothing).
//
// CONTRACT DEVIATION (entry kinds, recorded, not papered over). The
// ticket text calls for "ConversationJournalEntry types (TurnAppend,
// ThreadCreate, SegmentStateTransition)". internal/fleet/journal.Kind is
// R-21.216's CLOSED, P1-frozen eight-member enum (journal.go's own doc
// comment: "The Kind enum is CLOSED for P1 at exactly eight members");
// adding a ninth would be a T0 amendment this ticket has no authority to
// make, and journal.Store's own contract already carries the two kinds
// this domain's write path actually needs -- KindIntent (fsynced before
// the side effect) and KindAck (fsynced after). So the three named
// TYPES below are a discriminator INSIDE the Payload, not new journal.Kind
// values: every entry this package writes is a real KindIntent or
// KindAck, and journalPayload.Type says which of TurnAppend/ThreadCreate/
// SegmentStateTransition it records. ThreadCreate and SegmentStateTransition
// are declared for completeness (the closed vocabulary this domain's
// payloads use) but have no producer yet: AppendTurn's own thread-creation
// is an implicit side effect of the turn append (domain.go's AppendTurn
// doc comment), and this package defines no segment-state machine of its
// own to transition -- see internal/build/testonly-allow.json for the
// tracked entries.
//
// CONTRACT DEVIATION (corrupt-checkpoint fallback, recorded). This
// package implements NO corruption-detection or torn-tail-recovery logic
// of its own: Checkpoint and Resume call straight through to
// journal.Store's Checkpoint/Replay, both of which already run
// recoverEntityLocked internally (journal/replay.go, journal/checkpoint.go)
// -- the SAME real torn-tail scan and fallback-to-prior-good-checkpoint
// behavior internal/fleet/journal's own entry_recovery_test.go and
// checkpoint_test.go already prove against that package's real SQLite
// store. Re-deriving a synthetic corruption fixture here would need
// journal's private entryKey/fakeStore internals this package correctly
// has no access to, and would only re-test a contract journal.Store
// already tests for real. See journal_test.go's
// TestConversationCheckpoint_DelegatesRecovery for the direct proof that
// this package's Checkpoint/Resume genuinely call through to that layer
// rather than reimplementing it.
//
// SPORT: internal.conversation.journal/ADDED (P1-E20-W5-S44-T4).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The closed set of journalPayload.Type discriminators this domain
// writes or reserves. See this file's CONTRACT DEVIATION note.
const (
	JournalEntryTurnAppend             = "turn_append"
	JournalEntryThreadCreate           = "thread_create"
	JournalEntrySegmentStateTransition = "segment_state_transition"
)

// JournalStore is the seam this package journals through: journal.Store's
// Append/Checkpoint/Replay plus the HeadSeq peek internal/fleet/journal's
// own rpc.Reader interface declares (duck-typed, so this package never
// requires a concrete *journal.SQLiteStore -- *journal.SQLiteStore
// satisfies this today with no adapter code).
type JournalStore interface {
	Append(ctx context.Context, entityID string, kind journal.Kind, operationID string, payload json.RawMessage) (journal.Entry, error)
	Checkpoint(ctx context.Context, cursor journal.Cursor) error
	Replay(ctx context.Context, entityID string, cursor journal.Cursor, kinds []journal.Kind) ([]journal.Entry, error)
	HeadSeq(ctx context.Context, entityID string) (uint64, error)
}

// journalPayload is the wire shape of every entry this package appends:
// Type says which of the closed discriminators above it is, and Turn/
// Segments carry the operation's data (nil/empty for a type that does
// not use them).
type journalPayload struct {
	Type     string    `json:"type"`
	Turn     *Turn     `json:"turn,omitempty"`
	Segments []Segment `json:"segments,omitempty"`
}

// ErrJournalDecodeFailed is returned when a stored journal entry's
// Payload cannot be decoded back into journalPayload. Static message
// only -- see errors.go's PRIVACY doc comment; a decode error from
// encoding/json can otherwise echo a fragment of the malformed content.
var ErrJournalDecodeFailed = cascade.New(cascade.KindIntegrity, "conversation: journal entry payload decode failed")

// AppendTurnJournaled appends turn and its segments to store, journaling
// a TurnAppend KindIntent entry BEFORE the store write and a KindAck
// entry AFTER it commits. A journal write failure aborts before the
// store is ever touched (the intent case) or is returned as-is (the ack
// case, after the store commit already succeeded) -- in neither case
// does a partial store commit proceed without SOME journal record: the
// intent entry exists even when the ack does not, which is exactly what
// lets Resume tell "attempted but unconfirmed" apart from "completed"
// (Resume only replays KindAck entries).
func AppendTurnJournaled(ctx context.Context, js JournalStore, store Store, turn Turn, segments []Segment) error {
	payload, err := json.Marshal(journalPayload{Type: JournalEntryTurnAppend, Turn: &turn, Segments: segments})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "conversation: marshal journal payload")
	}
	if _, err := js.Append(ctx, turn.ThreadID, journal.KindIntent, turn.ID, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: journal intent write failed; no store write attempted")
	}
	if err := store.AppendTurn(ctx, turn); err != nil {
		return err
	}
	for _, seg := range segments {
		if err := store.AppendSegment(ctx, seg); err != nil {
			return err
		}
	}
	if _, err := js.Append(ctx, turn.ThreadID, journal.KindAck, turn.ID, payload); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: journal ack write failed after store commit")
	}
	return nil
}

// Checkpoint publishes threadID's current journal head as a checkpoint
// cursor. Idempotent by journal.Store.Checkpoint's own documented
// contract: re-checkpointing an unchanged head is a no-op, never an
// error and never a second write.
func Checkpoint(ctx context.Context, js JournalStore, threadID string) error {
	head, err := js.HeadSeq(ctx, threadID)
	if err != nil {
		return err
	}
	return js.Checkpoint(ctx, journal.Cursor{EntityID: threadID, Seq: head})
}

// checkpointJob returns a C/S-04.T4 scheduler-compatible entry point
// (internal/events/scheduler.Runnable is func(context.Context) error)
// that checkpoints threadID against js when invoked. See journal_test.go's
// TestcheckpointJob_SchedulerCompatible for the real scheduler.RegisterRunnable
// round trip proving the signature actually fits.
func checkpointJob(js JournalStore, threadID string) func(context.Context) error {
	return func(ctx context.Context) error {
		return Checkpoint(ctx, js, threadID)
	}
}

// Resume replays threadID's ACKed journal entries (Replay filtered to
// journal.KindAck: an unacknowledged intent never reached a committed
// store write, so it is never replayed -- see AppendTurnJournaled's own
// doc comment) and re-applies every TurnAppend against store, in order.
// A turn or segment store already holds (ErrImmutable, KindConflict) is
// treated as already-applied, not a failure: this is Resume's
// idempotence -- replaying the same journal twice returns a nonzero
// applied count the first time and 0 the second, with identical final
// store state either way.
func Resume(ctx context.Context, js JournalStore, store Store, threadID string) (applied int, err error) {
	entries, err := js.Replay(ctx, threadID, journal.Cursor{}, []journal.Kind{journal.KindAck})
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		var p journalPayload
		if jsonErr := json.Unmarshal(e.Payload, &p); jsonErr != nil {
			return applied, ErrJournalDecodeFailed
		}
		if p.Type != JournalEntryTurnAppend || p.Turn == nil {
			continue
		}
		n, err := resumeOneTurn(ctx, store, *p.Turn, p.Segments)
		if err != nil {
			return applied, err
		}
		applied += n
	}
	return applied, nil
}

// resumeOneTurn re-applies one journaled turn+segments against store,
// counting only the rows this call actually inserted (an
// already-present row -- ErrImmutable/KindConflict -- contributes 0).
func resumeOneTurn(ctx context.Context, store Store, turn Turn, segments []Segment) (int, error) {
	applied := 0
	if err := store.AppendTurn(ctx, turn); err != nil {
		if !cascade.HasKind(err, cascade.KindConflict) {
			return applied, err
		}
	} else {
		applied++
	}
	for _, seg := range segments {
		if err := store.AppendSegment(ctx, seg); err != nil {
			if !cascade.HasKind(err, cascade.KindConflict) {
				return applied, err
			}
			continue
		}
		applied++
	}
	return applied, nil
}
