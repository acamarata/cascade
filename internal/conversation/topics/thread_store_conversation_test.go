// Package topics (thread_store_conversation_test.go): Purpose: the REAL
// ThreadStore (NewConversationThreadStore, thread_store.go) exercised
// against a real modernc-sqlite conversation.Store - Art.2's real
// counterpart, never a self-authored double of the conversation domain.
// Split from thread_store_test.go only to keep both files under the
// 300-line cap (Art.10.3).
package topics

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newRealConversationStore opens a real sqlite database under t.TempDir(),
// applies the conversation domain's own migration set through its exported
// ApplyConversationSchema, and returns a Store over it - the same sequence
// internal/conversation's own tests use, built here from that package's
// EXPORTED API only (no _test symbol is reachable across packages, and none
// is needed).
func newRealConversationStore(t *testing.T) conversation.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topics-conversation-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := newFixedClock()
	if err := conversation.ApplyConversationSchema(
		context.Background(), db, migrate.SQLiteEmitter{}, migrate.Clock(clock), "", "",
	); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return conversation.NewStore(db)
}

func newRealThreadStore(t *testing.T) (ThreadStore, conversation.Store) {
	t.Helper()
	backing := newRealConversationStore(t)
	ts, err := NewConversationThreadStore(backing, newFixedClock())
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	return ts, backing
}

// TestConversationThreadStoreFilesTurnsAndText is the real implementation's
// round-trip: the turn lands in the conversation domain's own tables under
// the thread CreateOrSelect named, and its text lands as a text segment
// readable back through the same Store.
func TestConversationThreadStoreFilesTurnsAndText(t *testing.T) {
	ts, backing := newRealThreadStore(t)
	ctx := context.Background()
	threadID, err := ts.CreateOrSelect(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect: %v", err)
	}
	again, err := ts.CreateOrSelect(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect (second): %v", err)
	}
	if again != threadID {
		t.Fatalf("CreateOrSelect(code) = %q then %q, want the same ThreadID", threadID, again)
	}

	turn := Turn{Speaker: string(conversation.RoleUser), Text: "why does the build fail"}
	tt := ThreadTurn{ID: NewTopicTurnID(threadID, 0, turn), Turn: turn}
	if err := ts.AppendTurn(ctx, threadID, tt); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	turns, err := backing.ListTurns(ctx, string(threadID))
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != string(tt.ID) || turns[0].Role != conversation.RoleUser || turns[0].Seq != 0 {
		t.Fatalf("ListTurns = %+v, want exactly one user turn at seq 0 with id %q", turns, tt.ID)
	}
	segs, err := backing.ListSegments(ctx, string(tt.ID))
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segs) != 1 || segs[0].Content != turn.Text || segs[0].Kind != conversation.SegmentText {
		t.Fatalf("ListSegments = %+v, want one text segment carrying the turn's own text", segs)
	}
}

// TestConversationThreadStoreAppendTurnIsIdempotent is the real-store half
// of the re-delivery rule: the same ThreadTurn appended twice leaves one
// turn, and the second call is a no-op rather than a duplicate-id error.
func TestConversationThreadStoreAppendTurnIsIdempotent(t *testing.T) {
	ts, backing := newRealThreadStore(t)
	ctx := context.Background()
	threadID, err := ts.CreateOrSelect(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect: %v", err)
	}
	turn := Turn{Speaker: string(conversation.RoleAssistant), Text: "because the gate is red"}
	tt := ThreadTurn{ID: NewTopicTurnID(threadID, 0, turn), Turn: turn}
	for i := 0; i < 2; i++ {
		if err := ts.AppendTurn(ctx, threadID, tt); err != nil {
			t.Fatalf("AppendTurn (call %d): %v", i+1, err)
		}
	}
	turns, err := backing.ListTurns(ctx, string(threadID))
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("ListTurns = %d turns after appending the same ThreadTurn twice, want 1", len(turns))
	}
}

// TestConversationThreadStoreSequencesTurns proves the real store computes
// each turn's Seq from what the thread already holds, which is what the
// conversation domain's own out-of-order refusal requires.
func TestConversationThreadStoreSequencesTurns(t *testing.T) {
	ts, backing := newRealThreadStore(t)
	ctx := context.Background()
	threadID, err := ts.CreateOrSelect(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect: %v", err)
	}
	for i, text := range []string{"first", "second", "third"} {
		turn := Turn{Speaker: string(conversation.RoleUser), Text: text}
		if err := ts.AppendTurn(ctx, threadID, ThreadTurn{ID: NewTopicTurnID(threadID, i, turn), Turn: turn}); err != nil {
			t.Fatalf("AppendTurn(%d, %q): %v", i, text, err)
		}
	}
	turns, err := backing.ListTurns(ctx, string(threadID))
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("ListTurns = %d turns, want 3", len(turns))
	}
	for i, got := range turns {
		if got.Seq != int64(i) {
			t.Fatalf("turns[%d].Seq = %d, want %d", i, got.Seq, i)
		}
	}
}

func TestConversationThreadStoreRejectsUnknownSpeaker(t *testing.T) {
	ts, _ := newRealThreadStore(t)
	ctx := context.Background()
	threadID, err := ts.CreateOrSelect(ctx, TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect: %v", err)
	}
	turn := Turn{Speaker: "narrator", Text: "x"}
	err = ts.AppendTurn(ctx, threadID, ThreadTurn{ID: NewTopicTurnID(threadID, 0, turn), Turn: turn})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendTurn with an unmappable Speaker: error = %v, want KindInvalidInput", err)
	}
}

// TestConversationThreadStoreMoveTurnNamesTheMissingPrimitive pins the
// documented gap rather than letting a future reader assume a move happened:
// the refusal is typed KindUnsupported and says what the conversation domain
// does not offer.
func TestConversationThreadStoreMoveTurnNamesTheMissingPrimitive(t *testing.T) {
	ts, _ := newRealThreadStore(t)
	err := ts.MoveTurn(context.Background(), ThreadID("topic:general"), TurnID("u1"), TopicType("code"))
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("MoveTurn error = %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "turn-move primitive") {
		t.Fatalf("MoveTurn error = %q, want it to name the primitive the conversation domain lacks", err)
	}
}

func TestConversationThreadStoreRejectsBadInput(t *testing.T) {
	backing := newRealConversationStore(t)
	if _, err := NewConversationThreadStore(nil, newFixedClock()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewConversationThreadStore(nil store, ...) error = %v, want KindInvalidInput", err)
	}
	if _, err := NewConversationThreadStore(backing, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewConversationThreadStore(..., nil clock) error = %v, want KindInvalidInput", err)
	}
	ts, err := NewConversationThreadStore(backing, newFixedClock())
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	ctx := context.Background()
	for _, bad := range []TopicType{"", "has space", TopicType(strings.Repeat("x", topicTypeMaxLen+1))} {
		if _, err := ts.CreateOrSelect(ctx, bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("CreateOrSelect(%q) error = %v, want KindInvalidInput", bad, err)
		}
	}
	if err := ts.AppendTurn(ctx, "", ThreadTurn{ID: "x"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendTurn with an empty threadID: error = %v, want KindInvalidInput", err)
	}
	if err := ts.AppendTurn(ctx, "topic:code", ThreadTurn{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendTurn with an empty turn id: error = %v, want KindInvalidInput", err)
	}
}

// TestRouteTwiceOverRealStoreFilesEachTurnOnce is the end-to-end
// idempotence proof Art.1 asks for: the real AutoThreader over the real
// conversation-backed ThreadStore, the same window delivered twice, N turns
// filed rather than 2N.
func TestRouteTwiceOverRealStoreFilesEachTurnOnce(t *testing.T) {
	ts, backing := newRealThreadStore(t)
	at := mustAutoThreader(t, &fakeSegmenter{}, ts)
	ctx := context.Background()
	turns := []Turn{
		{Speaker: string(conversation.RoleUser), Text: "one"},
		{Speaker: string(conversation.RoleAssistant), Text: "two"},
		{Speaker: string(conversation.RoleUser), Text: "three"},
	}
	var threadIDs []ThreadID
	for i := 0; i < 2; i++ {
		got, err := at.Route(ctx, turns)
		if err != nil {
			t.Fatalf("Route (call %d): %v", i+1, err)
		}
		threadIDs = got
	}
	if len(threadIDs) != 1 {
		t.Fatalf("Route returned %d thread ids, want 1", len(threadIDs))
	}
	filed, err := backing.ListTurns(ctx, string(threadIDs[0]))
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(filed) != len(turns) {
		t.Fatalf("ListTurns = %d turns after routing the same %d-turn window twice, want %d",
			len(filed), len(turns), len(turns))
	}
}
