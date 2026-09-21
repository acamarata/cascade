// Package topics (thread_store_test.go): Purpose: fakeThreadStore, the
// shared ThreadStore test double auto_thread_test.go and reassign_test.go
// both use, plus NewTopicTurnID's own identity rules. Kept in the same file
// as the interface it doubles, matching segmenter_core_test.go's precedent
// of hosting shared doubles alongside the type they exercise. The REAL
// implementation's tests, against a real sqlite-backed conversation.Store,
// are in thread_store_conversation_test.go.
package topics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// moveCall records one MoveTurn invocation fakeThreadStore observed.
type moveCall struct {
	threadID ThreadID
	turnID   TurnID
	newType  TopicType
}

// fakeThreadStore is a minimal, deterministic ThreadStore double: one
// thread per TopicType, and injectable errors for each method so a caller's
// error-propagation path is exercisable without a real storage backend.
// Turn identity is the caller's (NewTopicTurnID), so this double dedups by
// id exactly as the real implementation does.
type fakeThreadStore struct {
	mu sync.Mutex

	threads map[TopicType]ThreadID
	turns   map[ThreadID][]ThreadTurn
	moves   []moveCall

	createErr error
	appendErr error
	moveErr   error

	threadSeq int
}

func newFakeThreadStore() *fakeThreadStore {
	return &fakeThreadStore{threads: make(map[TopicType]ThreadID), turns: make(map[ThreadID][]ThreadTurn)}
}

var _ ThreadStore = (*fakeThreadStore)(nil)

func (f *fakeThreadStore) CreateOrSelect(_ context.Context, topicType TopicType) (ThreadID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	if id, ok := f.threads[topicType]; ok {
		return id, nil
	}
	f.threadSeq++
	id := ThreadID(fmt.Sprintf("thread-%d", f.threadSeq))
	f.threads[topicType] = id
	return id, nil
}

func (f *fakeThreadStore) AppendTurn(_ context.Context, threadID ThreadID, turn ThreadTurn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	for _, existing := range f.turns[threadID] {
		if existing.ID == turn.ID {
			return nil
		}
	}
	f.turns[threadID] = append(f.turns[threadID], turn)
	return nil
}

func (f *fakeThreadStore) MoveTurn(_ context.Context, threadID ThreadID, turnID TurnID, newType TopicType) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, moveCall{threadID: threadID, turnID: turnID, newType: newType})
	return f.moveErr
}

// texts is the turn texts filed into threadID, in order - the shape most
// assertions in this package actually want.
func (f *fakeThreadStore) texts(threadID ThreadID) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.turns[threadID]))
	for _, t := range f.turns[threadID] {
		out = append(out, t.Turn.Text)
	}
	return out
}

func TestThreadStoreFakeCreateOrSelectIsStablePerTopic(t *testing.T) {
	f := newFakeThreadStore()
	first, err := f.CreateOrSelect(context.Background(), TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect: %v", err)
	}
	second, err := f.CreateOrSelect(context.Background(), TopicType("code"))
	if err != nil {
		t.Fatalf("CreateOrSelect (second): %v", err)
	}
	if first != second {
		t.Fatalf("CreateOrSelect(%q) = %q then %q, want the same ThreadID both times", "code", first, second)
	}
	other, err := f.CreateOrSelect(context.Background(), TopicType("general"))
	if err != nil {
		t.Fatalf("CreateOrSelect(general): %v", err)
	}
	if other == first {
		t.Fatalf("CreateOrSelect(general) = %q, want a different ThreadID than CreateOrSelect(code) = %q", other, first)
	}
}

func TestThreadStoreFakeAppendTurnRecordsInOrderAndDedups(t *testing.T) {
	f := newFakeThreadStore()
	threadID, _ := f.CreateOrSelect(context.Background(), TopicType("code"))
	one := ThreadTurn{ID: NewTopicTurnID(threadID, 0, Turn{Speaker: "user", Text: "one"}), Turn: Turn{Speaker: "user", Text: "one"}}
	two := ThreadTurn{ID: NewTopicTurnID(threadID, 1, Turn{Speaker: "user", Text: "two"}), Turn: Turn{Speaker: "user", Text: "two"}}
	for _, tt := range []ThreadTurn{one, two, one} {
		if err := f.AppendTurn(context.Background(), threadID, tt); err != nil {
			t.Fatalf("AppendTurn(%q): %v", tt.Turn.Text, err)
		}
	}
	got := f.texts(threadID)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("turns[%q] = %v, want [one two] in order with the repeat deduped", threadID, got)
	}
}

func TestThreadStoreFakeMoveTurnRecordsCall(t *testing.T) {
	f := newFakeThreadStore()
	if err := f.MoveTurn(context.Background(), ThreadID("t1"), TurnID("u1"), TopicType("general")); err != nil {
		t.Fatalf("MoveTurn: %v", err)
	}
	if len(f.moves) != 1 || f.moves[0] != (moveCall{threadID: "t1", turnID: "u1", newType: "general"}) {
		t.Fatalf("moves = %+v, want exactly one recorded call", f.moves)
	}
}

// TestNewTopicTurnIDIdentity pins the three properties AppendTurn's
// idempotence and MoveTurn's nameability both rest on: the same
// (thread, index, turn) is the same id; a different index, text, speaker or
// thread is a different id; and the id never contains the turn's text.
func TestNewTopicTurnIDIdentity(t *testing.T) {
	turn := Turn{Speaker: "user", Text: "a distinctive phrase"}
	base := NewTopicTurnID(ThreadID("topic:code"), 3, turn)
	if again := NewTopicTurnID(ThreadID("topic:code"), 3, turn); again != base {
		t.Fatalf("NewTopicTurnID is not deterministic: %q then %q", base, again)
	}
	for name, got := range map[string]TurnID{
		"different index":   NewTopicTurnID(ThreadID("topic:code"), 4, turn),
		"different thread":  NewTopicTurnID(ThreadID("topic:general"), 3, turn),
		"different text":    NewTopicTurnID(ThreadID("topic:code"), 3, Turn{Speaker: "user", Text: "another phrase"}),
		"different speaker": NewTopicTurnID(ThreadID("topic:code"), 3, Turn{Speaker: "assistant", Text: turn.Text}),
	} {
		if got == base {
			t.Fatalf("NewTopicTurnID collided for a %s: both %q", name, base)
		}
	}
	if strings.Contains(string(base), "distinctive") {
		t.Fatalf("NewTopicTurnID = %q, want an opaque digest that does not carry the turn's text", base)
	}
}
