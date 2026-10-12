//go:build !windows

package main

// Purpose: the composed daemon confines a caller principal to its session
// subtree at every depth: fleet.sessions.list rows and the fleet.sessions
// stream carry the caller's grandchildren and never a sibling subtree.
// No socket: dispatch is in-process through the composed registry, and the
// stream is the composed topic handler fed the predicate the composed
// middleware installs.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

const sessionScopeProbe = "test.session-scope.probe"

var _ = rpc.DeclareAccess(sessionScopeProbe, rpc.AccessOwnerView)

// lockedRecorder lets the test read a stream body while the handler writes.
type lockedRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func (w *lockedRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseRecorder.Write(p)
}

func (w *lockedRecorder) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Body.String()
}

func scopeCaller(session, parent string) context.Context {
	p := principal.Principal{ID: "p-" + session, Kind: principal.KindSession, SessionID: session,
		ParentSessionID: parent, Scope: "project:demo"}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func scopeDispatch(ctx context.Context, t *testing.T, reg *rpc.Registry, method string, params any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		t.Fatalf("%s %s: %+v", method, raw, eo)
	}
	out, _ := json.Marshal(res)
	return out
}

func scopeHook(ctx context.Context, t *testing.T, reg *rpc.Registry, session string, event hookpacks.HookEventType) {
	t.Helper()
	scopeDispatch(ctx, t, reg, hookpacks.MethodHookEvent, map[string]any{"harness": "claude-code",
		"event_type": string(event), "session_id": session, "pid": 1, "account": "acct",
		"timestamp_ms": time.Now().UnixMilli()})
}

func scopeList(ctx context.Context, t *testing.T, reg *rpc.Registry) string {
	t.Helper()
	var res struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(scopeDispatch(ctx, t, reg, sessions.MethodList, nil), &res); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res.Sessions {
		ids = append(ids, r.SessionID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func TestPrincipalSeesGrandchildSessionEvents(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	reg := c.Registry
	var installed func(string, string) bool
	reg.Register(sessionScopeProbe, func(ctx context.Context, _ json.RawMessage) (any, error) {
		installed, _ = rpc.EventVisibleFrom(ctx)
		return struct{}{}, nil
	})
	a, a1 := scopeCaller("a", ""), scopeCaller("a1", "a")
	// Each session is started by its own minted principal, which carries its parent.
	scopeHook(a, t, reg, "a", hookpacks.EventSessionStart)
	scopeHook(a1, t, reg, "a1", hookpacks.EventSubagentStart)
	scopeHook(scopeCaller("a1x", "a1"), t, reg, "a1x", hookpacks.EventSubagentStart)
	scopeHook(scopeCaller("s", ""), t, reg, "s", hookpacks.EventSessionStart)
	scopeHook(scopeCaller("s1", "s"), t, reg, "s1", hookpacks.EventSubagentStart)

	if got := scopeList(a, t, reg); got != "a,a1,a1x" {
		t.Fatalf("a list = %s, want a,a1,a1x", got)
	}
	if got := scopeList(a1, t, reg); got != "a1,a1x" {
		t.Fatalf("child list = %s, want a1,a1x (never its parent)", got)
	}
	scopeDispatch(a, t, reg, sessionScopeProbe, nil)
	if installed == nil || !installed("a1x", "") || installed("s1", "") || installed("s", "") {
		t.Fatal("composed middleware did not install the session-subtree predicate")
	}

	topic, ok := c.Events.Topics[sessions.Topic]
	if _, consumes := topic.(rpc.EventVisibleConsumer); !ok || !consumes {
		t.Fatal("composed fleet.sessions topic does not apply the event predicate")
	}
	serve := func(ctx context.Context, w *lockedRecorder) {
		topic.ServeHTTP(w, httptest.NewRequest("GET", rpc.EventsPath+"?topic="+sessions.Topic, nil).WithContext(ctx))
	}
	body := collectStream(installed, serve, func() { scopeHook(a, t, reg, "a", hookpacks.EventUserPromptSubmit) })
	if !strings.Contains(body, `"session_id":"a1x"`) {
		t.Fatalf("stream missed the grandchild:\n%s", body)
	}
	if strings.Contains(body, `"session_id":"s"`) || strings.Contains(body, `"session_id":"s1"`) {
		t.Fatalf("stream carried the sibling subtree:\n%s", body)
	}
}

// collectStream serves the topic under pred, runs trigger (whose UserPromptSubmit
// event is the last one published), and returns the body written up to it.
// The stream replays the namespace from its start.
func collectStream(pred func(string, string) bool, serve func(context.Context, *lockedRecorder), trigger func()) string {
	ctx, cancel := context.WithCancel(rpc.WithEventVisible(context.Background(), pred))
	w := &lockedRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		serve(ctx, w)
		close(done)
	}()
	trigger()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(w.body(), `"event_type":"UserPromptSubmit"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	return w.body()
}
