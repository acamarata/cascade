package conversation

// Purpose: real-entry-point tests for adapter_resume.go's chat.resume
//   method, driven through a real *rpc.Registry.Dispatch (never a bare
//   call to handleResume), matching adapter_test.go's own established
//   pattern for the other three methods.
// SPORT: internal.conversation.adapter/CHANGED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newTestAdapterWithStore mirrors newTestAdapter but also returns the
// Store, so a test can create a destination thread with its marker
// (CreateThread) before dispatching chat.resume against it --
// newTestAdapter's own store is unexported and unreachable otherwise.
func newTestAdapterWithStore(t *testing.T) (*Adapter, *rpc.Registry, Store) {
	t.Helper()
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	return adapter, registry, store
}

// fakeResumeHandler is a ResumeHandler double: it records the request it
// received and returns a pre-set result/error, so handleResume's own
// decode/encode logic is tested independently of resume.go's real
// orchestration (already covered by resume_test.go).
type fakeResumeHandler struct {
	got    ResumeRequest
	called bool
	result ResumeResult
	err    error
}

func (f *fakeResumeHandler) Resume(_ context.Context, req ResumeRequest) (ResumeResult, error) {
	f.got, f.called = req, true
	return f.result, f.err
}

// TestAdapterResume_Unconfigured_Refuses: the failing input is a request
// to a real adapter whose SetResume was never called -- ErrResumeService-
// Unavailable, never a nil-pointer panic.
func TestAdapterResume_Unconfigured_Refuses(t *testing.T) {
	_, registry, _ := newTestAdapter(t)
	_, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: "t1"})
	if errObj == nil {
		t.Fatal("expected an error when no ResumeHandler is wired")
	}
	if errObj.Code != cascade.RPCCodeUnavailable {
		t.Fatalf("errObj.Code = %d, want RPCCodeUnavailable (%d): %s", errObj.Code, cascade.RPCCodeUnavailable, errObj.Message)
	}
}

// TestAdapterResume_RoundTrip proves both directions of the wire mapping:
// resumeParams decodes into the exact ResumeRequest the handler receives
// (its SessionThreadID resolved into IntoTier from the stored marker), and
// the handler's ResumeResult encodes into the contract's wire names
// {thread_id, leading_context, tokens, trimmed[]}.
func TestAdapterResume_RoundTrip(t *testing.T) {
	adapter, registry, store := newTestAdapterWithStore(t)
	seedOwnedThread(t, store, "session1", provider.SensitivityPublic)
	fake := &fakeResumeHandler{result: ResumeResult{
		ThreadID: "th1", Context: "composed block", TokensUsed: 42, Budget: 4000,
		Trimmed: true, TrimReport: []string{"turn:x: budget exceeded"},
	}}
	adapter.SetResume(fake)

	got, errObj := dispatch(t, registry, MethodResume, resumeParams{
		TopicID: "th1", SessionThreadID: "session1",
	})
	if errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	if fake.got.TopicID != "th1" || fake.got.IntoTier != provider.SensitivityPublic {
		t.Fatalf("handler received %+v, want TopicID=th1 IntoTier=public (from the seeded session thread's own stored mode)", fake.got)
	}
	res, ok := got.(resumeResult)
	if !ok {
		t.Fatalf("result type = %T, want resumeResult", got)
	}
	if res.ThreadID != "th1" || res.LeadingContext != "composed block" || res.Tokens != 42 ||
		len(res.Trimmed) != 1 || res.Trimmed[0] != "turn:x: budget exceeded" {
		t.Fatalf("resumeResult = %+v, did not round-trip the handler's ResumeResult", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range []string{"thread_id", "leading_context", "tokens", "trimmed"} {
		if _, ok := fields[name]; !ok || len(fields) != 4 {
			t.Fatalf("wire fields = %s, want exactly thread_id, leading_context, tokens, trimmed", raw)
		}
	}
}

// resumeNoStoreReadSpy wraps a real Store and counts its reads, so
// TestAdapterResume_NoSessionThread_Refused can prove the "refused before
// any store read" ordering directly, not merely infer it from the error.
type resumeNoStoreReadSpy struct {
	Store
	threadPrivacyCalls int
	getThreadCalls     int
}

func (s *resumeNoStoreReadSpy) GetThread(ctx context.Context, id string) (Thread, bool, error) {
	s.getThreadCalls++
	return s.Store.GetThread(ctx, id)
}

func (s *resumeNoStoreReadSpy) ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error) {
	s.threadPrivacyCalls++
	return s.Store.ThreadPrivacy(ctx, threadID)
}

// TestAdapterResume_NoSessionThread_Refused (contract name): an absent
// session_thread_id is KindInvalidInput, refused BEFORE any store read and
// never defaulted to restricted. The failing input is the wire's omission
// of the field; resumeNoStoreReadSpy proves no store read happened.
func TestAdapterResume_NoSessionThread_Refused(t *testing.T) {
	spy := &resumeNoStoreReadSpy{Store: newTestStore(t)}
	bus := &fakeBus{}
	adapter := NewAdapter(spy, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	fake := &fakeResumeHandler{result: ResumeResult{Context: "should not be reached"}}
	adapter.SetResume(fake)

	_, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: "th1"})
	if errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("errObj = %+v, want RPCCodeInvalidInput", errObj)
	}
	if fake.called {
		t.Fatalf("handler was called with %+v despite the missing session_thread_id", fake.got)
	}
	if spy.threadPrivacyCalls != 0 || spy.getThreadCalls != 0 {
		t.Fatalf("store reads = %d ThreadPrivacy + %d GetThread, want 0 (refused before any store read)",
			spy.threadPrivacyCalls, spy.getThreadCalls)
	}
}

// TestAdapterResume_SessionThreadNotYetExisting_Refused proves a
// session_thread_id naming no thread is KindNotFound and the handler never
// runs: a new session makes its thread with chat.create_thread first, so
// the target tier is always a stored marker. Failing input: reading the
// unknown id as the restricted default.
func TestAdapterResume_SessionThreadNotYetExisting_Refused(t *testing.T) {
	adapter, registry, _ := newTestAdapterWithStore(t)
	fake := &fakeResumeHandler{result: ResumeResult{Context: "should not be reached"}}
	adapter.SetResume(fake)

	_, errObj := dispatch(t, registry, MethodResume, resumeParams{
		TopicID: "th1", SessionThreadID: "brand-new-never-seen-thread",
	})
	if errObj == nil || errObj.Code != cascade.RPCCodeNotFound {
		t.Fatalf("errObj = %+v, want RPCCodeNotFound", errObj)
	}
	if fake.called {
		t.Fatalf("handler was called with %+v for a nonexistent session thread", fake.got)
	}
}

// TestAdapterResume_QueryParams proves the query-path wire field also
// round-trips, since TestAdapterResume_RoundTrip only exercises TopicID.
func TestAdapterResume_QueryParams(t *testing.T) {
	adapter, registry, store := newTestAdapterWithStore(t)
	seedOwnedThread(t, store, "session1", provider.SensitivityRestricted)
	fake := &fakeResumeHandler{result: ResumeResult{Context: "query result"}}
	adapter.SetResume(fake)

	if _, errObj := dispatch(t, registry, MethodResume, resumeParams{Query: "find my keys", SessionThreadID: "session1"}); errObj != nil {
		t.Fatalf("dispatch: %+v", errObj)
	}
	if fake.got.Query != "find my keys" || fake.got.TopicID != "" {
		t.Fatalf("handler received %+v, want Query set and TopicID empty", fake.got)
	}
}

// TestAdapterResume_HandlerErrorPropagates proves handleResume returns
// the handler's own typed error unmodified, matching this file's own
// mapAdapterError identity-pass-through contract.
func TestAdapterResume_HandlerErrorPropagates(t *testing.T) {
	adapter, registry, store := newTestAdapterWithStore(t)
	seedOwnedThread(t, store, "session1", provider.SensitivityRestricted)
	adapter.SetResume(&fakeResumeHandler{err: errResumeTopicNotFound("missing")})

	_, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: "missing", SessionThreadID: "session1"})
	if errObj == nil || errObj.Code != cascade.RPCCodeNotFound {
		t.Fatalf("errObj = %+v, want RPCCodeNotFound", errObj)
	}
}

// TestAdapterResume_MalformedParams_Refused mirrors
// TestAdapter_MalformedPayload_Refused's established shape for this
// method: an unknown field and empty params are both refused before the
// handler is ever called.
func TestAdapterResume_MalformedParams_Refused(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	fake := &fakeResumeHandler{result: ResumeResult{Context: "should not be reached"}}
	adapter.SetResume(fake)

	cases := []json.RawMessage{
		json.RawMessage(`{"unknown_field":true}`),
		json.RawMessage(``),
	}
	for _, raw := range cases {
		_, errObj := registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: MethodResume, Params: raw})
		if errObj == nil {
			t.Errorf("params %q: expected an error", string(raw))
		}
		if errObj != nil && errObj.Code != cascade.RPCCodeInvalidInput {
			t.Errorf("params %q: errObj.Code = %d, want RPCCodeInvalidInput", string(raw), errObj.Code)
		}
	}
	if fake.called {
		t.Fatalf("handler was called with %+v despite malformed params", fake.got)
	}
}

// TestAdapterResume_WiredOnRealRegistry proves MethodResume is
// registered on the SAME real registry RegisterHandlers binds the other
// three methods onto -- not a second, parallel registry a caller would
// never reach through the daemon's real composition root
// (cmd/cascade/chat_wiring.go's adapter.RegisterHandlers(registry) call).
func TestAdapterResume_WiredOnRealRegistry(t *testing.T) {
	_, registry, _ := newTestAdapter(t)
	if !registry.Registered(MethodResume) {
		t.Fatalf("registry.Registered(%q) = false, want true (RegisterHandlers must bind chat.resume)", MethodResume)
	}
}
