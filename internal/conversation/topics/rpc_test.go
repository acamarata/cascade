// Package topics (rpc_test.go): Purpose: server-side behavioral coverage
//   for chat.topics_list and chat.threads_list (rpc.go) over a REAL
//   sqlite conversation.Store (Art.2), through the real *rpc.Registry
//   dispatch path — never a bare handler call that skips JSON-RPC
//   encode/decode.
// Inputs: newRealConversationStore/newFixedClock (shared test helpers,
//   thread_store_conversation_test.go / exemplar_store_test.go).
// Outputs: n/a (test file).
// Constraints: WINDOWS — every sqlite handle this file opens goes through
//   newRealConversationStore's own t.Cleanup(db.Close), registered before
//   t.TempDir()'s cleanup runs (Go runs cleanups in LIFO order, and
//   TempDir registers its own removal via t.Cleanup at the moment it is
//   called, which is BEFORE newRealConversationStore's db.Close
//   registration below it — so db.Close always runs first). No path
//   literals: every path comes from t.TempDir()/filepath.Join, matching
//   thread_store_conversation_test.go's own posture.
// SPORT: internal/conversation/topics rpc (TEST) (P1-E21-W5-S46-T4).

package topics

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newTestRPCHandlers builds RPCHandlers over a real sqlite
// conversation.Store, returning both so a test can seed data through the
// same store the handlers read.
func newTestRPCHandlers(t *testing.T) (*RPCHandlers, conversation.Store) {
	t.Helper()
	store := newRealConversationStore(t)
	h, err := NewRPCHandlers(store, newFixedClock())
	if err != nil {
		t.Fatalf("NewRPCHandlers: %v", err)
	}
	return h, store
}

// dispatch round-trips req through a real *rpc.Registry carrying h's two
// methods — proving the JSON-RPC encode/decode path, not just the Go
// method call.
func dispatch(t *testing.T, h *RPCHandlers, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	t.Helper()
	registry := rpc.NewRegistry()
	h.RegisterHandlers(registry)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = b
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
	if errObj != nil {
		return nil, errObj
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return b, nil
}

// seedTopicThread files n turns into topicType via the real ThreadStore
// (thread_store.go), returning the ThreadID -- the same path a composed
// AutoThreader.Route would use, so seeded fixtures look exactly like real
// engine output to the two handlers under test.
func seedTopicThread(t *testing.T, store conversation.Store, topicType string, n int) ThreadID {
	t.Helper()
	ts, err := NewConversationThreadStore(store, newFixedClock())
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

// TestHandleTopicsListCounts proves chat.topics_list reports one row per
// topic-prefixed thread with its real turn count, over the real store.
func TestHandleTopicsListCounts(t *testing.T) {
	h, store := newTestRPCHandlers(t)
	seedTopicThread(t, store, "code", 3)
	seedTopicThread(t, store, "general", 1)

	raw, errObj := dispatch(t, h, MethodTopicsList, nil)
	if errObj != nil {
		t.Fatalf("dispatch(%s): %+v", MethodTopicsList, errObj)
	}
	var got topicsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Topics) != 2 {
		t.Fatalf("Topics = %+v, want 2 rows", got.Topics)
	}
	byType := map[string]topicSummaryWire{}
	for _, r := range got.Topics {
		byType[r.TopicType] = r
	}
	if byType["code"].ThreadCount != 1 || byType["code"].TurnCount != 3 {
		t.Fatalf("code row = %+v, want thread_count=1 turn_count=3", byType["code"])
	}
	if byType["general"].TurnCount != 1 {
		t.Fatalf("general row = %+v, want turn_count=1", byType["general"])
	}
}

// TestHandleTopicsListEmptyStore proves an empty store answers an empty
// list, never an error — no topic thread has ever been created yet.
func TestHandleTopicsListEmptyStore(t *testing.T) {
	h, _ := newTestRPCHandlers(t)
	raw, errObj := dispatch(t, h, MethodTopicsList, nil)
	if errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	var got topicsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Topics == nil || len(got.Topics) != 0 {
		t.Fatalf("Topics = %+v, want an empty (non-nil-after-marshal) list", got.Topics)
	}
}

// TestHandleTopicsListRejectsUnknownField proves the no-params discipline
// matches handleListThreads' own DisallowUnknownFields posture.
func TestHandleTopicsListRejectsUnknownField(t *testing.T) {
	h, _ := newTestRPCHandlers(t)
	_, errObj := dispatch(t, h, MethodTopicsList, map[string]string{"bogus": "x"})
	if errObj == nil {
		t.Fatal("dispatch: want an error for an unknown field, got none")
	}
}

// TestHandleThreadsListPagination proves page/page_size slice the real,
// TopicType-sorted thread list, and total_count reflects the unsliced
// total.
func TestHandleThreadsListPagination(t *testing.T) {
	h, store := newTestRPCHandlers(t)
	seedTopicThread(t, store, "alpha", 1)
	seedTopicThread(t, store, "bravo", 2)
	seedTopicThread(t, store, "charlie", 5)

	raw, errObj := dispatch(t, h, MethodThreadsList, threadsListParams{Page: 1, PageSize: 2})
	if errObj != nil {
		t.Fatalf("dispatch page 1: %+v", errObj)
	}
	var page1 threadsListResult
	if err := json.Unmarshal(raw, &page1); err != nil {
		t.Fatalf("unmarshal page 1: %v", err)
	}
	if page1.TotalCount != 3 || len(page1.Threads) != 2 {
		t.Fatalf("page 1 = %+v, want total_count=3 len=2", page1)
	}
	if page1.Threads[0].TopicType != "alpha" || page1.Threads[1].TopicType != "bravo" {
		t.Fatalf("page 1 order = %+v, want [alpha bravo]", page1.Threads)
	}

	raw, errObj = dispatch(t, h, MethodThreadsList, threadsListParams{Page: 2, PageSize: 2})
	if errObj != nil {
		t.Fatalf("dispatch page 2: %+v", errObj)
	}
	var page2 threadsListResult
	if err := json.Unmarshal(raw, &page2); err != nil {
		t.Fatalf("unmarshal page 2: %v", err)
	}
	if len(page2.Threads) != 1 || page2.Threads[0].TopicType != "charlie" {
		t.Fatalf("page 2 = %+v, want exactly [charlie]", page2.Threads)
	}
	if page2.Threads[0].TurnCount != 5 {
		t.Fatalf("charlie turn_count = %d, want 5", page2.Threads[0].TurnCount)
	}
}

// TestHandleThreadsListDefaultParams proves absent params mean page 1,
// default page size — not an error.
func TestHandleThreadsListDefaultParams(t *testing.T) {
	h, store := newTestRPCHandlers(t)
	seedTopicThread(t, store, "solo", 1)

	raw, errObj := dispatch(t, h, MethodThreadsList, nil)
	if errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	var got threadsListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Page != 1 || got.PageSize != rpcPageDefault {
		t.Fatalf("defaults = page=%d page_size=%d, want page=1 page_size=%d", got.Page, got.PageSize, rpcPageDefault)
	}
}

// TestHandleThreadsListRejectsMalformedParams proves a decode failure
// (wrong type for page) is a typed error, never a silently-zeroed page.
func TestHandleThreadsListRejectsMalformedParams(t *testing.T) {
	h, _ := newTestRPCHandlers(t)
	_, errObj := dispatch(t, h, MethodThreadsList, map[string]string{"page": "not-a-number"})
	if errObj == nil {
		t.Fatal("dispatch: want an error for a malformed page field, got none")
	}
}

// TestHandleTopicsAndThreadsListExcludeLocalOnly is this file's privacy
// proof (D1): a local-only topic thread is never listed by either
// method, on a store this test deliberately treats as an
// "external-capable caller" would (no capability distinction exists at
// the RPC layer today, so the filter runs unconditionally — see rpc.go's
// header PRIVACY note for why that is the fail-closed choice, a superset
// of "never listed through an external-capable path").
func TestHandleTopicsAndThreadsListExcludeLocalOnly(t *testing.T) {
	h, store := newTestRPCHandlers(t)
	secretID := seedTopicThread(t, store, "secret", 2)
	seedTopicThread(t, store, "public", 1)

	if err := store.SetThreadPrivacy(context.Background(), string(secretID), provider.SensitivityLocalOnly); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}

	topicsRaw, errObj := dispatch(t, h, MethodTopicsList, nil)
	if errObj != nil {
		t.Fatalf("dispatch topics_list: %+v", errObj)
	}
	var topics topicsListResult
	if err := json.Unmarshal(topicsRaw, &topics); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range topics.Topics {
		if r.TopicType == "secret" {
			t.Fatalf("chat.topics_list leaked the local-only topic: %+v", topics.Topics)
		}
	}
	if len(topics.Topics) != 1 || topics.Topics[0].TopicType != "public" {
		t.Fatalf("topics = %+v, want exactly [public]", topics.Topics)
	}

	threadsRaw, errObj := dispatch(t, h, MethodThreadsList, threadsListParams{Page: 1, PageSize: 10})
	if errObj != nil {
		t.Fatalf("dispatch threads_list: %+v", errObj)
	}
	var threads threadsListResult
	if err := json.Unmarshal(threadsRaw, &threads); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if threads.TotalCount != 1 || len(threads.Threads) != 1 || threads.Threads[0].TopicType != "public" {
		t.Fatalf("chat.threads_list = %+v, want exactly [public] (secret excluded)", threads)
	}
}

// TestHandleTopicsAndThreadsListPropagatePrivacyReadFailure proves a
// ThreadPrivacy failure fails the whole call rather than silently reading
// as "not local-only" (fail closed on uncertainty, LANE-RULES §5).
func TestHandleTopicsAndThreadsListPropagatePrivacyReadFailure(t *testing.T) {
	backing := newRealConversationStore(t)
	seedTopicThread(t, backing, "will-fail", 1)

	failing := privacyFailingStore{Store: backing}
	threads, err := NewConversationThreadStore(failing, newFixedClock())
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	h := &RPCHandlers{store: failing, threads: threads}

	if _, errObj := dispatch(t, h, MethodTopicsList, nil); errObj == nil {
		t.Fatal("chat.topics_list: want an error when ThreadPrivacy fails, got none")
	}
	if _, errObj := dispatch(t, h, MethodThreadsList, nil); errObj == nil {
		t.Fatal("chat.threads_list: want an error when ThreadPrivacy fails, got none")
	}
}

// privacyFailingStore wraps a real conversation.Store, seeded with one
// topic thread via its own ListThreads/embedded methods, and refuses
// every ThreadPrivacy read — the error-path double this file's fail-
// closed assertion needs.
type privacyFailingStore struct {
	conversation.Store
}

func (privacyFailingStore) ThreadPrivacy(context.Context, string) (provider.SensitivityTier, error) {
	return provider.SensitivityRestricted, cascade.New(cascade.KindUnavailable, "topics: rpc_test: privacy store unavailable")
}
