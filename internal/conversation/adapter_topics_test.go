package conversation

// Purpose: adapter_topics.go's own unit tests, isolated from any real
//   topics-package composition: a fake TopicObserver proves
//   finishAppend/buildTopicWindow's contract (window ordering, the
//   just-appended turn's segments reused rather than re-fetched, the nil-
//   observer default, and that the append RESULT always carries whatever
//   TopicWarning the observer returned).
// SPORT: internal.conversation.adapter (ADD tests -- topic engine hook,
//   P1-E21-W5-S46-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// fakeTopicObserver records every Observe call's window (as flattened
// role:text pairs, never raw structs, so a test assertion cannot
// accidentally depend on TopicWindowTurn's field order) and returns a
// caller-configured warning.
type fakeTopicObserver struct {
	warning    string
	calls      int
	lastWindow []string
	lastThread string
}

func (f *fakeTopicObserver) Observe(_ context.Context, threadID string, window []TopicWindowTurn) string {
	f.calls++
	f.lastThread = threadID
	f.lastWindow = make([]string, len(window))
	for i, w := range window {
		f.lastWindow[i] = w.Role + ":" + w.Text
	}
	return f.warning
}

// dispatchAppend calls chat.append_turn through the real registry.Dispatch
// entry point (the shared dispatch helper in adapter_test.go) and fails
// the test on any RPC error, returning the decoded appendTurnResult.
func dispatchAppend(t *testing.T, registry *rpc.Registry, threadID, content string) appendTurnResult {
	t.Helper()
	res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: threadID, Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: content}},
	})
	if errObj != nil {
		t.Fatalf("chat.append_turn: %+v", errObj)
	}
	out, ok := res.(appendTurnResult)
	if !ok {
		t.Fatalf("chat.append_turn result = %#v, want appendTurnResult", res)
	}
	return out
}

func TestAdapter_TopicObserver_ObservedOnEveryAppend(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	observer := &fakeTopicObserver{}
	adapter.SetTopicObserver(observer)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	dispatchAppend(t, registry, "th1", "hello")
	if observer.calls != 1 {
		t.Fatalf("Observe calls = %d, want 1", observer.calls)
	}
	if observer.lastThread != "th1" || len(observer.lastWindow) != 1 || observer.lastWindow[0] != "user:hello" {
		t.Fatalf("Observe window = %q on thread %q, want [\"user:hello\"] on th1",
			observer.lastWindow, observer.lastThread)
	}

	dispatchAppend(t, registry, "th1", "second turn")
	if observer.calls != 2 {
		t.Fatalf("Observe calls = %d, want 2", observer.calls)
	}
	// WINDOW GROWS: the second call sees BOTH turns, oldest first -- the
	// contract auto_thread.go's own RE-DELIVERY IS IDEMPOTENT note
	// depends on (a caller always hands the segmenter the full window).
	want := []string{"user:hello", "user:second turn"}
	if len(observer.lastWindow) != 2 || observer.lastWindow[0] != want[0] || observer.lastWindow[1] != want[1] {
		t.Fatalf("Observe window = %q, want %q", observer.lastWindow, want)
	}
}

func TestAdapter_TopicObserver_WarningSurfacesOnResult(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	adapter.SetTopicObserver(&fakeTopicObserver{warning: "topics: something to report"})
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	res := dispatchAppend(t, registry, "th1", "hello")
	if res.TopicWarning != "topics: something to report" {
		t.Fatalf("TopicWarning = %q, want the observer's warning to pass through", res.TopicWarning)
	}
}

// TestAdapter_TopicObserver_NilIsTheDefault proves an Adapter with no
// SetTopicObserver call never invokes anything and always reports an
// empty TopicWarning -- the T2/embedded-mode default this ticket must
// not disturb.
func TestAdapter_TopicObserver_NilIsTheDefault(t *testing.T) {
	store := newTestStore(t)
	bus := &fakeBus{}
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	res := dispatchAppend(t, registry, "th1", "hello")
	if res.TopicWarning != "" {
		t.Fatalf("TopicWarning = %q, want empty with no TopicObserver wired", res.TopicWarning)
	}
}
