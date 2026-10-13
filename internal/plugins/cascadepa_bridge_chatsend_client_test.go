package plugins

// Purpose (this file): proves bridgeMirroringConversations' client-side
//   delegation to bridge.send (fix18 K1) — split from
//   cascadepa_bridge_chatsend_wiring_test.go (the daemon-side BridgeSend
//   proofs) to stay under Art.10.3's 300-line cap.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-wiring/TEST — fix18 round 2.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stubConversations is a minimal tools.Conversations fake: Append answers a
// canned result/error; the read methods answer canned values and record
// the argument they were given, so a passthrough can be proven, not assumed.
type stubConversations struct {
	res     tools.AppendResult
	err     error
	detail  tools.ThreadDetail
	threads []tools.ThreadSummary
	results []tools.SearchResult
	seen    []string
}

func (s *stubConversations) Append(context.Context, tools.AppendRequest) (tools.AppendResult, error) {
	return s.res, s.err
}
func (s *stubConversations) Thread(_ context.Context, id string) (tools.ThreadDetail, error) {
	s.seen = append(s.seen, "thread:"+id)
	return s.detail, nil
}
func (s *stubConversations) Threads(context.Context) ([]tools.ThreadSummary, error) {
	s.seen = append(s.seen, "threads")
	return s.threads, nil
}
func (s *stubConversations) Search(_ context.Context, req tools.SearchRequest) ([]tools.SearchResult, error) {
	s.seen = append(s.seen, "search:"+req.Query)
	return s.results, nil
}

var _ tools.Conversations = (*stubConversations)(nil)

// recordingBridgeSendDoer is a minimal rpcDoer fake recording every
// bridge.send call's params — the client-side counterpart to
// recordingChatSender, driven through the SAME seam production's
// bridgeSendClient uses (Art.7.2: no real socket in an untagged test, see
// this file's header and plugin_rpc_bridge_send_test.go's identical note).
type recordingBridgeSendDoer struct {
	calls []BridgeSendParams
	err   error
	res   *BridgeSendResult // nil answers Mirrored
}

func (d *recordingBridgeSendDoer) Do(_ context.Context, method string, params, out any) error {
	if method != MethodBridgeSend {
		return cascade.Newf(cascade.KindInternal, "unexpected method %q", method)
	}
	d.calls = append(d.calls, params.(BridgeSendParams))
	if d.err != nil {
		return d.err
	}
	if res, ok := out.(*BridgeSendResult); ok {
		*res = BridgeSendResult{Mirrored: true}
		if d.res != nil {
			*res = *d.res
		}
	}
	return nil
}

// TestBridgeMirroringConversations_BridgedThread_CallsBridgeSendRPC proves
// fix18's actual route (K1): a successful Append calls bridge.send over
// the rpcDoer seam with the same thread id, content and sensitivity.
// Mutation: remove Append's c.send.send(...) call and this goes RED (zero
// calls recorded) — this is the "drop the client call" mutation K1 names.
func TestBridgeMirroringConversations_BridgedThread_CallsBridgeSendRPC(t *testing.T) {
	doer := &recordingBridgeSendDoer{}
	inner := &stubConversations{res: tools.AppendResult{ThreadID: bridgedThreadID, TurnID: "turn-1"}}
	conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: doer}}

	res, err := conv.Append(context.Background(), tools.AppendRequest{
		ThreadID: bridgedThreadID, Role: "user", Content: "hello owner", Sensitivity: tools.SensitivityPublic,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if res.TurnID != "turn-1" {
		t.Fatalf("Append result = %+v, want the inner adapter's own result unchanged", res)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("bridge.send calls = %+v, want exactly one", doer.calls)
	}
	want := BridgeSendParams{ThreadID: bridgedThreadID, Content: "hello owner", Sensitivity: tools.SensitivityPublic}
	if doer.calls[0] != want {
		t.Fatalf("bridge.send params = %+v, want %+v", doer.calls[0], want)
	}
	if res.Bridge == nil || res.Bridge.Status != tools.BridgeMirrored {
		t.Fatalf("Append result Bridge = %+v, want mirrored", res.Bridge)
	}
}

// TestBridgeMirroringConversations_AppendFailure_NeverCallsRPC proves a
// failed inner Append never dials bridge.send, and its own error is
// returned unchanged.
func TestBridgeMirroringConversations_AppendFailure_NeverCallsRPC(t *testing.T) {
	doer := &recordingBridgeSendDoer{}
	wantErr := cascade.New(cascade.KindUnavailable, "test: inner append failed")
	inner := &stubConversations{err: wantErr}
	conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: doer}}

	_, err := conv.Append(context.Background(), tools.AppendRequest{ThreadID: bridgedThreadID, Role: "user", Content: "hi"})
	if err != wantErr {
		t.Fatalf("Append error = %v, want the inner adapter's own error unchanged", err)
	}
	if len(doer.calls) != 0 {
		t.Fatalf("bridge.send calls = %+v, want zero after a failed Append", doer.calls)
	}
}

// TestBridgeMirroringConversations_RPCFailure_StillReturnsAppendResult
// proves a bridge.send failure is logged, not propagated: the already-
// recorded turn's own Append result/error must reach the caller unchanged.
func TestBridgeMirroringConversations_RPCFailure_StillReturnsAppendResult(t *testing.T) {
	doer := &recordingBridgeSendDoer{err: cascade.New(cascade.KindUnavailable, "test: bridge.send refused")}
	inner := &stubConversations{res: tools.AppendResult{ThreadID: bridgedThreadID, TurnID: "turn-4"}}
	conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: doer}}

	res, err := conv.Append(context.Background(), tools.AppendRequest{ThreadID: bridgedThreadID, Role: "user", Content: "hi"})
	if err != nil {
		t.Fatalf("Append: %v, want the mirror failure swallowed", err)
	}
	if res.TurnID != "turn-4" {
		t.Fatalf("Append result = %+v, want the inner adapter's own result unchanged", res)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("bridge.send calls = %+v, want exactly one (failed) attempt", doer.calls)
	}
	if res.Bridge == nil || res.Bridge.Status != tools.BridgeFailed {
		t.Fatalf("Append result Bridge = %+v, want failed (visible to the tool caller)", res.Bridge)
	}
}

// TestBridgeMirroringConversations_ReadPassthroughs proves Thread/Threads/
// Search reach inner WITH the caller's argument and return inner's values:
// a decorator answering zero values, or a fixed id, turns this RED.
func TestBridgeMirroringConversations_ReadPassthroughs(t *testing.T) {
	inner := &stubConversations{
		detail:  tools.ThreadDetail{ThreadID: "t1", Turns: []tools.TurnRecord{{TurnID: "u1"}}},
		threads: []tools.ThreadSummary{{ID: "t1", Title: "notes"}},
		results: []tools.SearchResult{{ThreadID: "t1", TurnID: "u1", Content: "q hit"}},
	}
	conv := newBridgeMirroringConversations(inner)
	ctx := context.Background()

	if got, err := conv.Thread(ctx, "t1"); err != nil || got.ThreadID != "t1" || len(got.Turns) != 1 {
		t.Fatalf("Thread = %+v, %v; want inner's detail", got, err)
	}
	if got, err := conv.Threads(ctx); err != nil || len(got) != 1 || got[0].Title != "notes" {
		t.Fatalf("Threads = %+v, %v; want inner's listing", got, err)
	}
	if got, err := conv.Search(ctx, tools.SearchRequest{Query: "q"}); err != nil || len(got) != 1 || got[0].Content != "q hit" {
		t.Fatalf("Search = %+v, %v; want inner's results", got, err)
	}
	if want := []string{"thread:t1", "threads", "search:q"}; strings.Join(inner.seen, ",") != strings.Join(want, ",") {
		t.Fatalf("inner saw %v, want %v", inner.seen, want)
	}
}

// --- bridgeSendClient's own rpc()/send() branches (real transport, no doer) --

// TestBridgeSendClient_RPC_PathResolutionFailure proves rpc() surfaces a
// resolvePaths failure, mirroring bridgeChatService's identical proof
// (cascadepa_bridge_chat_wiring_test.go).
func TestBridgeSendClient_RPC_PathResolutionFailure(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "boom: no home directory")
	c := bridgeSendClient{resolvePaths: func() (runtime.PathProvider, error) { return nil, wantErr }}
	if _, err := c.rpc(); err == nil || !containsAll(err.Error(), "boom: no home directory") {
		t.Fatalf("rpc: err = %v, want it to name the path-resolution failure", err)
	}
}

// TestBridgeSendClient_RPC_ResolvesRealClientWhenNoDoer proves a nil doer
// falls through to a real client.Client construction (the production
// path), never a nil rpcDoer.
func TestBridgeSendClient_RPC_ResolvesRealClientWhenNoDoer(t *testing.T) {
	socket := missingSocketPath(t)
	c := bridgeSendClient{dial: client.UnixDialer, timeout: time.Second,
		resolvePaths: func() (runtime.PathProvider, error) { return fakePathProvider{socket: socket}, nil }}
	doer, err := c.rpc()
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if doer == nil {
		t.Fatal("rpc: doer = nil, want a real client.Client")
	}
}

// TestBridgeSendClient_Send_TransportUnreachable_NeverPanics proves send's
// real-transport failure path (Art.7.2-safe: dialing a unix socket nothing
// binds, mirroring TestBridgeChatService_AppendTurn_TransportUnreachable)
// is logged and reported as a failed outcome, never returned as an error.
func TestBridgeSendClient_Send_TransportUnreachable_NeverPanics(t *testing.T) {
	socket := missingSocketPath(t)
	c := bridgeSendClient{dial: client.UnixDialer, timeout: time.Second,
		resolvePaths: func() (runtime.PathProvider, error) { return fakePathProvider{socket: socket}, nil }}
	if got := c.send(context.Background(), bridgedThreadID, "hi", tools.SensitivityPublic); got.Status != tools.BridgeFailed {
		t.Fatalf("send over an unreachable socket = %+v, want failed", got)
	}
}
