// Purpose: the bridge's DURABLE chat-to-thread map. A bridge chat gets its
//   conversation thread from the daemon (chat.create_thread, or adoption of
//   the chat's legacy thread), and this store remembers which thread that
//   was, in both directions, across restarts.
//
// Inputs: a bridge subject, a chat id and a thread id; the same BridgeState
//   store the pairing binding lives in (no new database file).
//
// Outputs: ThreadFor (chat -> thread), ChatFor (thread -> chat) and Bind,
//   a CREATE-ONLY write: the first binding of a chat wins and every later
//   Bind of that chat returns it.
//
// Constraints:
//   - ROW SHAPE. A binding is two SubjectState rows under reserved subject
//     keys that no bridge subject can take (subjects are "tg-<hex>"):
//     "threadmap/<subject>/chat/<id>" holds the thread id and
//     "threadmap/<subject>/thread/<thread>" holds the chat id, each in the
//     CodeDigest column of a row whose TrustTier is threadMapTier and whose
//     AllowedFrom is EMPTY. An empty allowlist means SubjectState.Bound() and
//     Allows() are false for every map row, so a map key can never pass the
//     pairing gate, whatever sender is asked about. A row of any other shape
//     reads as corrupt (KindIntegrity), never as a binding: a reader fails
//     closed rather than guessing.
//   - CREATE-ONLY. Both rows are written with Version 0, which BridgeState
//     turns into an insert that refuses when a row exists (ErrStateConflict).
//     Nothing here ever updates or deletes a map row.
//   - ORDER. The chat row is the commit point and is written first, so the
//     loser of a race returns the winner's thread without writing anything.
//     The thread row follows; a Bind that finds the chat row already
//     present re-asserts it, so a crash between the two writes heals on the
//     next Bind. Until then ChatFor misses, which refuses outbound sends.
//   - No owner check lives here: the transport adapter (telegram.ThreadMap)
//     decides WHO may bind; this store only keeps what it was told, once.
//
// SPORT: plugins/cascade-pa ThreadMapStore (chat-to-thread map).

package cascadepa

import (
	"context"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// threadMapTier marks a map row; no binding row ever carries it.
const threadMapTier = "chat-thread-map"

// ErrThreadMapCorrupt is the refusal for a map row whose shape is not one
// Bind writes. Exported so a host test can assert it by identity.
var ErrThreadMapCorrupt = cascade.New(cascade.KindIntegrity,
	"cascade-pa: a chat-to-thread map row is malformed; the bridge refuses to use it")

// ErrThreadBoundElsewhere refuses binding a thread that is already bound to
// another chat: one thread never answers for two chats.
var ErrThreadBoundElsewhere = cascade.New(cascade.KindConflict,
	"cascade-pa: the thread is already bound to another chat")

// ThreadMapStore is the durable chat-to-thread map over BridgeState.
type ThreadMapStore struct {
	state BridgeState
}

// NewThreadMapStore builds the map over state (nil -> fail-closed: every
// read and write refuses with ErrNoBridgeState).
func NewThreadMapStore(state BridgeState) *ThreadMapStore {
	return &ThreadMapStore{state: orUnconfigured(state)}
}

func chatMapKey(subject string, chatID int64) string {
	return "threadmap/" + subject + "/chat/" + strconv.FormatInt(chatID, 10)
}

func threadMapKey(subject, threadID string) string {
	return "threadmap/" + subject + "/thread/" + threadID
}

// ThreadFor returns the thread chatID is bound to.
func (s *ThreadMapStore) ThreadFor(ctx context.Context, subject string, chatID int64) (string, bool, error) {
	return s.read(ctx, chatMapKey(subject, chatID))
}

// ChatFor returns the chat threadID is bound to. A thread id with no row is
// (0, false, nil): not a bridge thread.
func (s *ThreadMapStore) ChatFor(ctx context.Context, subject, threadID string) (int64, bool, error) {
	if threadID == "" {
		return 0, false, nil
	}
	raw, ok, err := s.read(ctx, threadMapKey(subject, threadID))
	if err != nil || !ok {
		return 0, false, err
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(chatID, 10) != raw {
		return 0, false, ErrThreadMapCorrupt
	}
	return chatID, true, nil
}

// Bind records chatID -> threadID once and returns the thread chatID is
// bound to afterwards: threadID if this call created the binding, the
// existing thread if one was already there. See this file's ORDER note.
func (s *ThreadMapStore) Bind(ctx context.Context, subject string, chatID int64, threadID string) (string, error) {
	if subject == "" || threadID == "" {
		return "", cascade.New(cascade.KindInvalidInput, "cascade-pa: a thread binding needs a subject and a thread id")
	}
	if owner, ok, err := s.ChatFor(ctx, subject, threadID); err != nil {
		return "", err
	} else if ok && owner != chatID {
		return "", ErrThreadBoundElsewhere
	}
	bound, err := s.createOnce(ctx, chatMapKey(subject, chatID), threadID)
	if err != nil {
		return "", err
	}
	chat := strconv.FormatInt(chatID, 10)
	back, err := s.createOnce(ctx, threadMapKey(subject, bound), chat)
	if err != nil {
		return "", err
	}
	if back != chat {
		return "", ErrThreadBoundElsewhere
	}
	return bound, nil
}

// createOnce writes value under key unless a row exists, and returns the
// value the key holds afterwards (the existing one on a lost race).
func (s *ThreadMapStore) createOnce(ctx context.Context, key, value string) (string, error) {
	err := s.state.Save(ctx, SubjectState{Subject: key, TrustTier: threadMapTier, CodeDigest: value})
	if err == nil {
		return value, nil
	}
	if !IsStateConflict(err) {
		return "", err
	}
	stored, ok, rerr := s.read(ctx, key)
	if rerr != nil {
		return "", rerr
	}
	if !ok {
		return "", ErrThreadMapCorrupt
	}
	return stored, nil
}

// read loads key's single value, refusing any row Bind did not write.
func (s *ThreadMapStore) read(ctx context.Context, key string) (string, bool, error) {
	st, ok, err := s.state.Load(ctx, key)
	if err != nil || !ok {
		return "", false, err
	}
	if st.TrustTier != threadMapTier || len(st.AllowedFrom) != 0 || st.CodeDigest == "" || mapRowTouchedByPairing(st) {
		return "", false, ErrThreadMapCorrupt
	}
	return st.CodeDigest, true, nil
}

// mapRowTouchedByPairing reports whether a pairing write has left its marks
// on a map row (an expiry, a wrong-attempt count or a paired time). Bind
// never writes those, so such a row reads as corrupt rather than as a value.
func mapRowTouchedByPairing(st SubjectState) bool {
	return !st.CodeExpiresAt.IsZero() || st.WrongAttempts != 0 || !st.PairedAt.IsZero()
}
