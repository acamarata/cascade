package conversation

// Purpose: real-entry-point tests for adapter.go's three JSON-RPC
//   methods, driven through a real *rpc.Registry.Dispatch (never a bare
//   call to the handler function), plus the ticket's explicit trap: proof
//   the adapter offers no back door around T1's append-only invariant.
// SPORT: internal.conversation.adapter/ADDED (tests) (P1-E20-W5-S43-T2).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testClock is a fixed conversation.Clock for adapter tests.
type testClock struct{ t time.Time }

func (c testClock) Now() time.Time { return c.t }

func newAdapterTestClock() Clock { return testClock{t: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)} }

// dispatch is the shared helper every test below uses to drive the REAL
// entry point: registry.Dispatch, not a to-the-side call to a handler
// method.
func dispatch(t *testing.T, registry *rpc.Registry, method string, params any) (any, *rpc.ErrorObject) {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		raw = b
	}
	return registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
}

func newTestAdapter(t *testing.T) (*Adapter, *rpc.Registry, *fakeBus) {
	t.Helper()
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	return adapter, registry, bus
}

func TestAdapter_AppendGetList_RoundTrip(t *testing.T) {
	_, registry, bus := newTestAdapter(t)

	appendResult, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "hello"}},
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn errored: %+v", errObj)
	}
	res, ok := appendResult.(appendTurnResult)
	if !ok || res.TurnID == "" || res.ThreadID != "th1" {
		t.Fatalf("chat.append_turn result = %#v", appendResult)
	}
	if len(bus.published) != 1 {
		t.Fatalf("CLIENT-LOCAL ECHO: bus.published = %d, want 1 (echo before response)", len(bus.published))
	}

	getResult, errObj := dispatch(t, registry, MethodGetThread, getThreadParams{ThreadID: "th1"})
	if errObj != nil {
		t.Fatalf("chat.get_thread errored: %+v", errObj)
	}
	gr, ok := getResult.(getThreadResult)
	if !ok || len(gr.Turns) != 1 || len(gr.Turns[0].Segments) != 1 {
		t.Fatalf("chat.get_thread result = %#v", getResult)
	}

	listResult, errObj := dispatch(t, registry, MethodListThreads, nil)
	if errObj != nil {
		t.Fatalf("chat.list_threads errored: %+v", errObj)
	}
	lr, ok := listResult.(listThreadsResult)
	if !ok || len(lr.Threads) != 1 || lr.Threads[0].ID != "th1" {
		t.Fatalf("chat.list_threads result = %#v", listResult)
	}
}

// TestAdapter_CannotMutateHistory is the ticket's explicit append-only
// trap. It has two halves:
//
//  1. Through the adapter's own wire surface, chat.append_turn has no
//     "seq" or "turn_id" parameter at all -- handleAppendTurn always
//     computes the next seq from the thread's current length (adapter.go)
//     -- so there is structurally no request shape a caller can send
//     through this JSON-RPC method that names an already-occupied
//     position. Two calls in a row land at seq 0 and seq 1, never a
//     collision.
//  2. The underlying Store this adapter wraps still refuses a genuine
//     replay at the storage layer (T1's own structural guarantee): a
//     direct AppendTurn call reusing seq 0 gets ErrImmutable. This proves
//     the backstop the adapter relies on is real, not merely that the
//     adapter's own request shape happens not to expose it.
func TestAdapter_CannotMutateHistory(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	first, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "first"}},
	})
	if errObj != nil {
		t.Fatalf("seed append errored: %+v", errObj)
	}
	firstTurn := first.(appendTurnResult)
	if firstTurn.Seq != 0 {
		t.Fatalf("first append landed at seq %d, want 0", firstTurn.Seq)
	}

	second, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "assistant",
		Segments: []appendSegmentWire{{Kind: "text", Content: "second"}},
	})
	if errObj != nil {
		t.Fatalf("second append errored: %+v", errObj)
	}
	if second.(appendTurnResult).Seq != 1 {
		t.Fatalf("second append landed at seq %d, want 1 -- the adapter never requests an occupied slot", second.(appendTurnResult).Seq)
	}

	// Half 2: the store-level backstop, called directly (not through the
	// adapter, which has no way to construct this request), must still
	// refuse a replay of the exact turn already committed.
	replay := Turn{ID: firstTurn.TurnID, ThreadID: "th1", Seq: 0, Role: RoleUser, CreatedAt: newAdapterTestClock().Now().Unix()}
	if err := store.AppendTurn(context.Background(), replay); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("store.AppendTurn replay = %v, want ErrImmutable (KindConflict)", err)
	}
}

func TestAdapter_MalformedPayload_Refused(t *testing.T) {
	_, registry, _ := newTestAdapter(t)
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodAppendTurn, Params: json.RawMessage(`{"unknown_field":true}`),
	})
	if errObj == nil {
		t.Fatalf("expected an error for an unknown-field payload")
	}
	if errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("errObj.Code = %d, want RPCCodeInvalidInput (%d)", errObj.Code, cascade.RPCCodeInvalidInput)
	}
}

func TestAdapter_MalformedPayload_MoreShapes(t *testing.T) {
	_, registry, _ := newTestAdapter(t)

	// chat.append_turn: empty params.
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodAppendTurn,
	}); errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("empty append_turn params = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.append_turn: missing role.
	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{ThreadID: "th1"}); errObj == nil ||
		errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("missing-role append_turn = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.append_turn: unknown role.
	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{ThreadID: "th1", Role: "bogus"}); errObj == nil ||
		errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("unknown-role append_turn = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.append_turn: unknown segment kind.
	if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "bogus", Content: "x"}},
	}); errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("unknown segment kind = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.get_thread: empty params.
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodGetThread,
	}); errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("empty get_thread params = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.get_thread: unknown field.
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodGetThread, Params: json.RawMessage(`{"bogus":1}`),
	}); errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("unknown-field get_thread params = %+v, want RPCCodeInvalidInput", errObj)
	}

	// chat.list_threads: unknown field is rejected too.
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: MethodListThreads, Params: json.RawMessage(`{"bogus":1}`),
	}); errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("unknown-field list_threads params = %+v, want RPCCodeInvalidInput", errObj)
	}
}

func TestAdapter_GetThread_MultiTurnWithSegments(t *testing.T) {
	_, registry, _ := newTestAdapter(t)
	for i := 0; i < 2; i++ {
		if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
			ThreadID: "th1", Role: "user",
			Segments: []appendSegmentWire{{Kind: "text", Content: "a"}, {Kind: "code", Content: "b"}},
		}); errObj != nil {
			t.Fatalf("append %d errored: %+v", i, errObj)
		}
	}
	result, errObj := dispatch(t, registry, MethodGetThread, getThreadParams{ThreadID: "th1"})
	if errObj != nil {
		t.Fatalf("get_thread errored: %+v", errObj)
	}
	gr := result.(getThreadResult)
	if len(gr.Turns) != 2 || len(gr.Turns[0].Segments) != 2 {
		t.Fatalf("get_thread result = %#v", gr)
	}
}

func TestAdapter_GetThread_NotFound(t *testing.T) {
	_, registry, _ := newTestAdapter(t)
	_, errObj := dispatch(t, registry, MethodGetThread, getThreadParams{ThreadID: "does-not-exist"})
	if errObj == nil || errObj.Code != cascade.RPCCodeNotFound {
		t.Fatalf("chat.get_thread on missing thread = %+v, want RPCCodeNotFound", errObj)
	}
}

// TestAdapter_EmbeddedMode_AppendsWithoutSSE proves the Windows tier-2
// documented behavior: chat.append_turn still succeeds and persists the
// turn, but publishes nothing.
func TestAdapter_EmbeddedMode_AppendsWithoutSSE(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{failWith: cascade.New(cascade.KindUnavailable, "must never be reached in embedded mode")}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), ModeEmbedded)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	result, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "embedded"}},
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn in embedded mode errored: %+v", errObj)
	}
	if _, ok := result.(appendTurnResult); !ok {
		t.Fatalf("result = %#v, want appendTurnResult", result)
	}
	if len(bus.published) != 0 {
		t.Fatalf("embedded mode must never publish, got %d", len(bus.published))
	}
	turns, err := store.ListTurns(context.Background(), "th1")
	if err != nil || len(turns) != 1 {
		t.Fatalf("turn not persisted in embedded mode: turns=%v err=%v", turns, err)
	}
}

// TestAdapter_SSEWriteFailure_Propagates proves a non-embedded SSE
// failure surfaces to the caller (CLIENT-LOCAL ECHO's own requirement)
// rather than being reported as success while the turn is committed but
// never echoed.
func TestAdapter_SSEWriteFailure_Propagates(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{failWith: cascade.New(cascade.KindUnavailable, "socket write failed")}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: "th1", Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "x"}},
	})
	if errObj == nil {
		t.Fatalf("expected chat.append_turn to fail when the SSE mirror fails")
	}
	if errObj.Code != cascade.RPCCodeUnavailable {
		t.Fatalf("errObj.Code = %d, want RPCCodeUnavailable", errObj.Code)
	}
}
