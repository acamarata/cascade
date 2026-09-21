// Package topics (rpc_order_test.go): Purpose: the two rpc.go
//
//	assertions the S-46.T4 confirming review found missing: (1) that
//	chat.threads_list' TopicType order is produced by rpc.go's own
//	sort.Slice (:286), not an accident of sqlite returning rows in
//	insertion order -- the reviewer's own mutation (deleting that
//	sort.Slice call, restoring after) left TestHandleThreadsListPagination
//	passing because that test seeds alpha/bravo/charlie in already-sorted
//	insertion order; this file seeds out of order so the same mutation
//	goes RED here; (2) that LastActivity reflects a real, distinct turn
//	timestamp -- specifically the THREAD's most recent turn, not its
//	first -- which no existing test in this package asserted. Split from
//	rpc_test.go only because that file is already at the 300-line cap
//	(Art.10.3), not a topical split.
//
// Inputs: the same newTestRPCHandlers/dispatch/seedTopicThread helpers
//
//	rpc_test.go defines (same package, one test binary); a local
//	seedTopicThreadWithClock variant for the cases that need to control
//	turn timestamps rather than accept newFixedClock's single constant
//	instant.
//
// Outputs: n/a (test file).
// Constraints: WINDOWS -- every sqlite handle here comes from
//
//	newRealConversationStore's own t.Cleanup(db.Close) (thread_store_
//	conversation_test.go), registered after t.TempDir()'s own cleanup so
//	Close always runs first (Go's LIFO cleanup order); no path literals.
//
// SPORT: internal/conversation/topics rpc (TEST) (P1-E21-W5-S46-T4, FIX
//
//	pass after the confirming review's FIX 4).
package topics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
)

// seedTopicThreadWithClock is seedTopicThread's sibling for tests that
// need to control WHEN each turn lands, not just how many -- rpc_test.go's
// own seedTopicThread always builds a fresh newFixedClock() (a single
// constant instant), which cannot produce two threads, or two turns of
// the same thread, with different CreatedAt values. clock is shared
// across calls so a test can advance clock.now between them and see that
// reflected in the real store's CreatedAt column.
func seedTopicThreadWithClock(t *testing.T, store conversation.Store, topicType string, n int, clock *fixedClock) ThreadID {
	t.Helper()
	ts, err := NewConversationThreadStore(store, clock)
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	ctx := context.Background()
	threadID, err := ts.CreateOrSelect(ctx, TopicType(topicType))
	if err != nil {
		t.Fatalf("CreateOrSelect(%s): %v", topicType, err)
	}
	for i := 0; i < n; i++ {
		turn := Turn{Speaker: string(conversation.RoleUser), Text: topicType}
		tt := ThreadTurn{ID: NewTopicTurnID(threadID, i, turn), Turn: turn}
		if err := ts.AppendTurn(ctx, threadID, tt); err != nil {
			t.Fatalf("AppendTurn(%s, %d): %v", topicType, i, err)
		}
	}
	return threadID
}

// TestHandleThreadsListOrderIsSortedNotInsertionOrder pins rpc.go's
// header claim ("sorts the result by TopicType so threads_list'
// pagination is stable across calls") against a fixture the confirming
// review's own mutation exposes and TestHandleThreadsListPagination's
// fixture does not: conversation.Store.ListThreads orders rows by
// `created_at ASC, id ASC` (internal/conversation/store.go), and this
// package's own topic thread ids are "topic:"+TopicType, so on a TIED
// created_at (rpc_test.go's seedTopicThread always builds a fresh
// newFixedClock() -- one constant instant, every call), the store's own
// `id ASC` tiebreak ALREADY returns rows in TopicType order regardless of
// listTopicThreads' sort.Slice (:286) -- which is exactly why the
// reviewer's mutation (deleting that sort.Slice) left
// TestHandleThreadsListPagination passing. This test breaks that tie on
// purpose: zulu is created FIRST (earliest created_at), alpha LAST
// (latest), so the store's own physical order is [zulu, mike, alpha] --
// the opposite of TopicType order. Only rpc.go's own sort.Slice can turn
// that into the asserted [alpha, mike, zulu].
func TestHandleThreadsListOrderIsSortedNotInsertionOrder(t *testing.T) {
	store := newRealConversationStore(t)
	clock := &fixedClock{now: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)}
	seedTopicThreadWithClock(t, store, "zulu", 1, clock)
	clock.now = clock.now.Add(time.Hour)
	seedTopicThreadWithClock(t, store, "mike", 1, clock)
	clock.now = clock.now.Add(time.Hour)
	seedTopicThreadWithClock(t, store, "alpha", 1, clock)

	h, err := NewRPCHandlers(store, newFixedClock())
	if err != nil {
		t.Fatalf("NewRPCHandlers: %v", err)
	}
	raw, errObj := dispatch(t, h, MethodThreadsList, threadsListParams{Page: 1, PageSize: 10})
	if errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	var got threadsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Threads) != 3 {
		t.Fatalf("Threads = %+v, want 3 rows", got.Threads)
	}
	wantOrder := []string{"alpha", "mike", "zulu"}
	for i, want := range wantOrder {
		if got.Threads[i].TopicType != want {
			t.Fatalf("Threads = %+v, want TopicType order %v (created in the opposite order: zulu, mike, alpha)",
				got.Threads, wantOrder)
		}
	}
}

// TestHandleThreadsListLastActivityIsMostRecentTurn proves LastActivity
// is the thread's LAST turn's CreatedAt, not its first, and not a zero
// value -- rpc.go:282 reads turns[n-1].CreatedAt, but nothing in
// rpc_test.go asserted LastActivity at all before this file. Two turns
// are appended 48 hours apart on the same clock; a LastActivity equal to
// the first turn's time (or zero) would mean the wrong index, or no
// timestamp propagation, and this test would fail either way.
func TestHandleThreadsListLastActivityIsMostRecentTurn(t *testing.T) {
	store := newRealConversationStore(t)
	clock := &fixedClock{now: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)}
	firstTurnUnix := clock.now.Unix()

	threadID := seedTopicThreadWithClock(t, store, "drift", 1, clock)

	clock.now = clock.now.Add(48 * time.Hour)
	lastTurnUnix := clock.now.Unix()
	ts, err := NewConversationThreadStore(store, clock)
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	turn := Turn{Speaker: string(conversation.RoleUser), Text: "drift"}
	tt := ThreadTurn{ID: NewTopicTurnID(threadID, 1, turn), Turn: turn}
	if err := ts.AppendTurn(context.Background(), threadID, tt); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if firstTurnUnix == lastTurnUnix {
		t.Fatal("test bug: clock did not advance between turns")
	}

	h, err := NewRPCHandlers(store, newFixedClock())
	if err != nil {
		t.Fatalf("NewRPCHandlers: %v", err)
	}
	raw, errObj := dispatch(t, h, MethodThreadsList, threadsListParams{Page: 1, PageSize: 10})
	if errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	var got threadsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Threads) != 1 {
		t.Fatalf("Threads = %+v, want exactly 1 row", got.Threads)
	}
	row := got.Threads[0]
	if row.LastActivity != lastTurnUnix {
		t.Fatalf("LastActivity = %d, want %d (the second turn's time, not the first turn's %d or zero)",
			row.LastActivity, lastTurnUnix, firstTurnUnix)
	}
}
