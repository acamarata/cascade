// Purpose: ThreadID, TurnID, ThreadTurn (the identifiers and record shape
//   this ticket's sub-systems share), the ThreadStore interface AutoThreader
//   and Reassign depend on, and conversationThreadStore - the real
//   ThreadStore, filing turns into the conversation domain's own
//   append-only tables through internal/conversation.Store.
// Inputs: a conversation.Store and a Clock for the real implementation;
//   contracts and identifier shapes only for the rest.
// Outputs: a ThreadID per topic, or a typed error propagated from the
//   conversation domain unmodified.
// Constraints: ThreadStore stays an interface so AutoThreader and Reassign
//   can be exercised against a double without a database (R-14.64), but the
//   interface is not the only implementation this ticket ships: Art.1
//   requires a real one, and NewConversationThreadStore is it - the
//   composition root wires it at startup.
//
//   TURN IDENTITY is caller-computed, matching
//   internal/conversation.NewTurnID's own posture (a turn's id is a
//   content address its caller derives, never a value the store hands
//   back): AppendTurn takes a ThreadTurn that already carries its TurnID,
//   and returns only an error, exactly the shape this ticket's contract
//   names. That is also what makes re-delivery of the same window
//   idempotent - see NewTopicTurnID and AutoThreader.Route.
// SPORT: internal/conversation/topics thread-store (ADD) (P1-E21-W5-S45-T3).

package topics

import (
	"context"
	"strconv"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ThreadID identifies one topic thread a ThreadStore implementation
// manages. Opaque to this package's callers: only the implementation
// interprets its shape.
type ThreadID string

// TurnID identifies one turn within a thread. Content-addressed by
// NewTopicTurnID, so the same turn at the same position in the same
// re-delivered window always carries the same id.
type TurnID string

// ThreadTurn is one turn as a ThreadStore records it: the segmenter-side
// Turn plus the content-addressed id its caller computed for it. This
// mirrors internal/conversation.Turn, whose own ID field is likewise set
// by the caller (via NewTurnID) rather than assigned by the store - the
// precedent that lets AppendTurn return a bare error and still leave the
// turn nameable by a later MoveTurn call.
type ThreadTurn struct {
	// ID is the turn's identity within its thread, from NewTopicTurnID.
	ID TurnID
	// Turn is the segmenter-side turn itself (speaker and text).
	Turn Turn
}

// NewTopicTurnID is the content address of turn at index within threadID:
// the same (thread, index, speaker, text) always yields the same TurnID,
// which is what makes AppendTurn idempotent for a re-delivered window.
//
// IDENTITY IS (thread, window index, speaker, text), stated plainly
// because the boundary matters: re-delivering the SAME window is a no-op,
// while a differently-aligned window that repeats a turn at a different
// index is a different identity. This package has no cross-window turn
// identity to consult, and collapsing two genuinely repeated turns (the
// same short reply twice) would lose real data, so index is part of the
// address rather than content alone.
//
// Text is hashed, never carried: the digest is an opaque key and is never
// interpolated into a log line, an error message, or a metric label, the
// same rule internal/conversation/domain.go's PRIVACY note sets for
// conversation content. (domain.go's NewSegmentID excludes Content for a
// different reason - two segments at the same (turnID, seq, kind) ARE the
// same segment by construction, which does not hold here, since a routed
// window's position in a thread is not knowable to Route.)
func NewTopicTurnID(threadID ThreadID, index int, turn Turn) TurnID {
	return TurnID(blake3Hex("topic-turn", string(threadID), strconv.Itoa(index), turn.Speaker, turn.Text))
}

// ThreadStore files turns into topic threads. Declared as an interface so
// AutoThreader.Route and Reassign can be tested against a double rather
// than a real storage backend (R-14.64) - the same shape this package's own
// Segmenter interface (segmenter_types.go) already uses.
// NewConversationThreadStore is the real implementation.
type ThreadStore interface {
	// CreateOrSelect returns the ThreadID of the thread that owns
	// topicType, creating one if none exists yet for that topic. Two
	// calls with the same topicType against the same store return the
	// same ThreadID; an unavailable backing store returns a typed
	// error, never a zero-value ThreadID mistaken for a real one.
	CreateOrSelect(ctx context.Context, topicType TopicType) (ThreadID, error)

	// LookupThread reports which thread owns topicType WITHOUT creating
	// one: (id, true, nil) when a thread for that topic already exists,
	// ("", false, nil) when none does, and a typed error when the backing
	// store cannot answer - the same failure CreateOrSelect would have
	// raised, never a false "none". This is the read-only half of
	// CreateOrSelect, for a caller that must report what routing WOULD do
	// without performing it (ObserveLogger.Observe, observe_log.go). An
	// implementation MUST return the same ThreadID CreateOrSelect would
	// return for that topic; a proposal derived any other way would name a
	// thread the pipeline does not use.
	LookupThread(ctx context.Context, topicType TopicType) (ThreadID, bool, error)

	// AppendTurn records turn as the next turn in threadID. A turn whose
	// ID is already present in threadID is a no-op, not an error: the
	// same window delivered twice must not double-file its turns.
	// Appending to a threadID CreateOrSelect never returned is
	// implementation-defined (a typed error is expected, never a silent
	// create); this package's own callers always pass through
	// CreateOrSelect first.
	AppendTurn(ctx context.Context, threadID ThreadID, turn ThreadTurn) error

	// MoveTurn reassigns turnID (currently in threadID) to the thread
	// selected for newType, creating that thread if needed via the same
	// rule CreateOrSelect uses. An implementation whose backing domain
	// has no move primitive refuses with a typed error naming what is
	// missing, never a silent no-op.
	MoveTurn(ctx context.Context, threadID ThreadID, turnID TurnID, newType TopicType) error
}

// topicThreadIDPrefix namespaces a topic thread's id so it can never
// collide with a thread id another producer (the chat surface, a resume
// flow) writes into the same conversation_thread table.
const topicThreadIDPrefix = "topic:"

// conversationThreadStore is the real ThreadStore: one thread per
// TopicType in the conversation domain's own conversation_thread /
// conversation_turn / conversation_segment tables, reached only through
// internal/conversation.Store's exported API - no SQL of its own.
type conversationThreadStore struct {
	store conversation.Store
	clock Clock
}

// NewConversationThreadStore wraps a real conversation.Store as a
// ThreadStore. clock is required rather than read from the wall clock
// (Art.7: no bare time.Now in domain logic), and both dependencies are
// refused when nil, matching every other constructor in this package.
func NewConversationThreadStore(store conversation.Store, clock Clock) (ThreadStore, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"topics: NewConversationThreadStore: conversation.Store must not be nil")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"topics: NewConversationThreadStore: Clock must not be nil")
	}
	return &conversationThreadStore{store: store, clock: clock}, nil
}

// CreateOrSelect resolves topicType's thread id and proves the backing
// store can answer for it. The id is deterministic, so the same topic
// always selects the same thread.
//
// The conversation domain exposes no create-thread call (store.go's own
// AppendTurn comment records that gap: the thread row is written by the
// first append's ON CONFLICT DO NOTHING ensure-thread step), so the
// "create" half of this method is that first AppendTurn rather than a call
// made here. The GetThread probe is not decoration: a backing store that
// cannot answer is refused at selection time instead of surfacing later as
// a confusing append failure.
func (s *conversationThreadStore) CreateOrSelect(ctx context.Context, topicType TopicType) (ThreadID, error) {
	if ctx == nil {
		return "", cascade.New(cascade.KindInvalidInput, "topics: CreateOrSelect: ctx must not be nil")
	}
	if err := validateTopicType(topicType); err != nil {
		return "", err
	}
	id := ThreadID(topicThreadIDPrefix + string(topicType))
	if _, _, err := s.store.GetThread(ctx, string(id)); err != nil {
		return "", err
	}
	return id, nil
}

// LookupThread answers CreateOrSelect's question without the create half:
// the thread id is the same deterministic value CreateOrSelect computes, and
// the conversation domain's GetThread already distinguishes "no such
// thread" (found false, nil error) from "cannot answer" (a typed error), so
// this method reports the first as ("", false, nil) and propagates the
// second unchanged. A topic whose thread row does not exist yet is the
// normal case before the first AppendTurn writes it.
func (s *conversationThreadStore) LookupThread(ctx context.Context, topicType TopicType) (ThreadID, bool, error) {
	if ctx == nil {
		return "", false, cascade.New(cascade.KindInvalidInput, "topics: LookupThread: ctx must not be nil")
	}
	if err := validateTopicType(topicType); err != nil {
		return "", false, err
	}
	id := ThreadID(topicThreadIDPrefix + string(topicType))
	_, found, err := s.store.GetThread(ctx, string(id))
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return id, true, nil
}

// AppendTurn files turn into threadID as a conversation turn plus one text
// segment carrying its content. A turn whose ID is already in the thread
// is a no-op: uniqueness is checked here, in code, against the thread's
// own turn list, so a re-delivered window neither double-files nor
// surfaces the domain's duplicate-id conflict as a caller-visible failure.
func (s *conversationThreadStore) AppendTurn(ctx context.Context, threadID ThreadID, turn ThreadTurn) error {
	if ctx == nil {
		return cascade.New(cascade.KindInvalidInput, "topics: AppendTurn: ctx must not be nil")
	}
	if threadID == "" || turn.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "topics: AppendTurn: threadID and turn.ID must not be empty")
	}
	role, roleErr := conversation.DecodeRole(turn.Turn.Speaker)
	if roleErr != nil {
		return cascade.Wrap(cascade.KindInvalidInput, roleErr,
			"topics: AppendTurn: a turn's Speaker must name a conversation role")
	}
	existing, err := s.store.ListTurns(ctx, string(threadID))
	if err != nil {
		return err
	}
	for _, t := range existing {
		if t.ID == string(turn.ID) {
			return nil
		}
	}
	now := s.clock.Now().Unix()
	if err := s.store.AppendTurn(ctx, conversation.Turn{
		ID: string(turn.ID), ThreadID: string(threadID),
		Seq: int64(len(existing)), Role: role, CreatedAt: now,
	}); err != nil {
		return err
	}
	return s.appendText(ctx, turn, now)
}

// appendText writes turn's content as the single text segment of the turn
// just appended, split out of AppendTurn to keep it under the 50-line cap.
// Content reaches storage verbatim and nothing else - never a log line,
// never an error message (domain.go's PRIVACY rule).
func (s *conversationThreadStore) appendText(ctx context.Context, turn ThreadTurn, now int64) error {
	return s.store.AppendSegment(ctx, conversation.Segment{
		ID:        conversation.NewSegmentID(string(turn.ID), 0, conversation.SegmentText),
		TurnID:    string(turn.ID),
		Seq:       0,
		Kind:      conversation.SegmentText,
		Content:   turn.Turn.Text,
		CreatedAt: now,
	})
}

// MoveTurn refuses, with the reason named. The conversation domain has no
// turn-move primitive to call: internal/conversation.Store offers
// AppendTurn, AppendSegment, the read paths, ArchiveThread/UnarchiveThread
// and PruneTurns only, and a turn row is append-only by construction
// (domain.go: "immutable once appended", its id content-addressed on
// thread/seq/role). Performing the move by other means would mean raw SQL
// against another domain's tables, and reporting success without moving
// anything would be worse than this refusal - so the gap is surfaced as a
// typed KindUnsupported error and recorded for the owner rather than
// papered over. See .github/wiki/Topic-Engine.md § Reassign.
func (s *conversationThreadStore) MoveTurn(
	_ context.Context, threadID ThreadID, turnID TurnID, newType TopicType,
) error {
	if err := validateTopicType(newType); err != nil {
		return err
	}
	return cascade.Newf(cascade.KindUnsupported,
		"topics: MoveTurn: moving turn %q out of thread %q into topic %q needs a turn-move primitive the "+
			"conversation domain does not expose (its turn rows are append-only with content-addressed ids)",
		turnID, threadID, newType)
}
