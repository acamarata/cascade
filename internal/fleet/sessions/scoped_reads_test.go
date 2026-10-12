//go:build !windows

package sessions_test

// Purpose: a caller principal sees and drives only its own session subtree
// through fleet.sessions.list, fleet.sessions.hook_event and the
// fleet.sessions stream. Sessions are created through the production
// hook_event verb with minted principals, never by direct store writes.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// scopeFixture is one registry serving the production session verbs behind
// the session-scope middleware, plus a minter for principals.
type scopeFixture struct {
	reg    *rpc.Registry
	store  *sessions.Store
	bus    *events.Bus
	clock  *testkit.FrozenClock
	minter *principal.Minter
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	kv := storetest.NewMemStore()
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	store := sessions.New(kv, clock, bus)
	f := &scopeFixture{reg: rpc.NewRegistry(), store: store, bus: bus, clock: clock, minter: principal.NewMinter(clock)}
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, clock.Now()) })
	if err := hookpacks.RegisterHookEventHandler(f.reg, store, bus, clock); err != nil {
		t.Fatal(err)
	}
	sessions.RegisterHandlers(f.reg, store)
	f.reg.Use(sessions.ScopeMiddleware(store, sessions.SessionBinding{
		Methods:     []string{hookpacks.MethodHookEvent},
		Creator:     hookpacks.MethodHookEvent,
		StartEvents: []string{string(hookpacks.EventSessionStart), string(hookpacks.EventSubagentStart)},
	}))
	return f
}

// as mints a principal for session (child of parent) and returns its caller context.
func (f *scopeFixture) as(t *testing.T, session, parent string) context.Context {
	t.Helper()
	token, err := f.minter.Mint(principal.Principal{ID: "p-" + session, Kind: principal.KindSession,
		SessionID: session, ParentSessionID: parent, Scope: "project:demo"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.minter.Resolve(token, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func owner() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func (f *scopeFixture) call(ctx context.Context, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	raw, _ := json.Marshal(params)
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		return nil, eo
	}
	out, _ := json.Marshal(res)
	return out, nil
}

// hook posts one hook event for session as ctx's caller.
func (f *scopeFixture) hook(ctx context.Context, session string, event hookpacks.HookEventType) *rpc.ErrorObject {
	_, eo := f.call(ctx, hookpacks.MethodHookEvent, map[string]any{
		"harness": "claude-code", "event_type": string(event), "session_id": session,
		"pid": 1, "account": "acct", "timestamp_ms": f.clock.Now().UnixMilli(),
	})
	return eo
}

// listIDs returns the session ids fleet.sessions.list gives ctx's caller.
func (f *scopeFixture) listIDs(ctx context.Context, t *testing.T, params any) []string {
	t.Helper()
	out, eo := f.call(ctx, sessions.MethodList, params)
	if eo != nil {
		t.Fatalf("fleet.sessions.list: %+v", eo)
	}
	var res struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range res.Sessions {
		ids = append(ids, r.SessionID)
	}
	sort.Strings(ids)
	return ids
}

// seedTree builds a / a1 / a1x and b / b1 through hook_event: each session
// is started by its own minted principal, which carries its parent.
func (f *scopeFixture) seedTree(t *testing.T) {
	t.Helper()
	steps := []struct {
		ctx     context.Context
		session string
		event   hookpacks.HookEventType
	}{
		{f.as(t, "a", ""), "a", hookpacks.EventSessionStart},
		{f.as(t, "a1", "a"), "a1", hookpacks.EventSubagentStart},
		{f.as(t, "a1x", "a1"), "a1x", hookpacks.EventSubagentStart},
		{f.as(t, "b", ""), "b", hookpacks.EventSessionStart},
		{f.as(t, "b1", "b"), "b1", hookpacks.EventSubagentStart},
	}
	for _, s := range steps {
		if eo := f.hook(s.ctx, s.session, s.event); eo != nil {
			t.Fatalf("seed %s: %+v", s.session, eo)
		}
	}
}

func equalIDs(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func TestPrincipalSeesOnlyOwnSessions(t *testing.T) {
	f := newScopeFixture(t)
	f.seedTree(t)
	a := f.as(t, "a", "")
	equalIDs(t, "owner list", f.listIDs(owner(), t, nil), "a", "a1", "a1x", "b", "b1")
	equalIDs(t, "a list", f.listIDs(a, t, nil), "a", "a1", "a1x")
	equalIDs(t, "a list naming b", f.listIDs(a, t, map[string]string{"parent_session_id": "b"}))
	equalIDs(t, "a list naming a", f.listIDs(a, t, map[string]string{"parent_session_id": "a"}), "a1")
	equalIDs(t, "b list", f.listIDs(f.as(t, "b", ""), t, nil), "b", "b1")
	equalIDs(t, "child list", f.listIDs(f.as(t, "a1", "a"), t, nil), "a1", "a1x")
	equalIDs(t, "sessionless principal", f.listIDs(f.as(t, "", ""), t, nil))

	ctx, cancel := context.WithCancel(context.Background())
	streamA, doneA := runSSE(rpc.WithEventVisible(ctx, f.store.Visibility(principal.Principal{SessionID: "a"})), sessions.NewSSEHandler(f.bus, f.clock), "?topic=fleet.sessions")
	streamB, doneB := runSSE(rpc.WithEventVisible(ctx, f.store.Visibility(principal.Principal{SessionID: "b"})), sessions.NewSSEHandler(f.bus, f.clock), "?topic=fleet.sessions")
	for _, w := range []*syncRecorder{streamA, streamB} {
		waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	}
	for _, s := range []string{"b1", "b", "a1x", "a"} {
		if eo := f.hook(owner(), s, hookpacks.EventUserPromptSubmit); eo != nil {
			t.Fatal(s, eo)
		}
	}
	waitFor(t, func() bool { _, body, _ := streamA.snapshot(); return strings.Contains(body, `"session_id":"a"`) })
	waitFor(t, func() bool { _, body, _ := streamB.snapshot(); return strings.Contains(body, `"session_id":"b"`) })
	cancel()
	<-doneA
	<-doneB
	_, bodyA, _ := streamA.snapshot()
	_, bodyB, _ := streamB.snapshot()
	if n := strings.Count(bodyA, `"session_id":"b`); n != 0 {
		t.Fatalf("a's stream wrote %d b events:\n%s", n, bodyA)
	}
	if !strings.Contains(bodyA, `"session_id":"a1x"`) {
		t.Fatalf("a's stream missed its grandchild:\n%s", bodyA)
	}
	if n := strings.Count(bodyB, `"session_id":"a`); n != 0 {
		t.Fatalf("b's stream wrote %d a events:\n%s", n, bodyB)
	}
}

func TestHookEventForeignSessionRefused(t *testing.T) {
	f := newScopeFixture(t)
	f.seedTree(t)
	before, err := f.store.List(context.Background(), sessions.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := f.bus.SubscribeFromHead(context.Background(), sessions.Topic, "probe", 16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	a := f.as(t, "a", "")
	for _, c := range []struct {
		session string
		event   hookpacks.HookEventType
	}{{"b", hookpacks.EventUserPromptSubmit}, {"b1", hookpacks.EventSessionEnd}, {"b", hookpacks.EventSessionStart},
		{"b1", hookpacks.EventSubagentStart}, {"ghost", hookpacks.EventPreToolUse}, {"ghost", hookpacks.EventSessionStart}} {
		eo := f.hook(a, c.session, c.event)
		if eo == nil || eo.Code != cascade.RPCCodePermissionDenied || !strings.HasSuffix(eo.Message, "sessions: session outside caller scope") {
			t.Fatalf("a %s on %s: %+v, want the uniform scope refusal", c.event, c.session, eo)
		}
	}
	for _, ev := range []hookpacks.HookEventType{hookpacks.EventStop, hookpacks.EventSessionStart} {
		if eo := f.hook(f.as(t, "a1", "a"), "a", ev); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
			t.Fatalf("child %s on its parent: %+v", ev, eo)
		}
	}
	after, err := f.store.List(context.Background(), sessions.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(before)
	a1, _ := json.Marshal(after)
	if string(b1) != string(a1) || len(after) != 5 {
		t.Fatalf("refused hook events changed the store:\nbefore %s\nafter  %s", b1, a1)
	}
	if eo := f.hook(a, "a1x", hookpacks.EventUserPromptSubmit); eo != nil {
		t.Fatalf("a on its own grandchild: %+v", eo)
	}
	select {
	case ev := <-sub.Events:
		if !strings.Contains(string(ev.Payload), `"session_id":"a1x"`) {
			t.Fatalf("first event after refusals is %s, want the admitted a1x event", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admitted hook event published nothing")
	}
}

// TestStartEventKeepsParentLink: a repeated start event never detaches a
// session from its parent, so a child cannot hide from its parent's view.
func TestStartEventKeepsParentLink(t *testing.T) {
	f := newScopeFixture(t)
	f.seedTree(t)
	child := f.as(t, "a1", "a")
	if eo := f.hook(child, "a1", hookpacks.EventSessionStart); eo != nil {
		t.Fatal(eo)
	}
	if eo := f.hook(owner(), "a1x", hookpacks.EventSubagentStart); eo != nil {
		t.Fatal(eo)
	}
	equalIDs(t, "a list after restarts", f.listIDs(f.as(t, "a", ""), t, nil), "a", "a1", "a1x")
	if eo := f.hook(owner(), "solo", hookpacks.EventSessionStart); eo != nil {
		t.Fatal(eo)
	}
	rec, err := f.store.Get(context.Background(), "solo")
	if err != nil || rec.ParentSessionID != nil {
		t.Fatalf("owner-created session got parent %v (%v)", rec.ParentSessionID, err)
	}
}

// TestChildCannotCaptureAbsentParent: a child whose parent session is not
// stored yet cannot create it (and so cannot read it or close a cycle); the
// parent's own later start links nothing back to the child.
func TestChildCannotCaptureAbsentParent(t *testing.T) {
	f := newScopeFixture(t)
	child := f.as(t, "c", "p")
	if eo := f.hook(child, "c", hookpacks.EventSessionStart); eo != nil {
		t.Fatal(eo)
	}
	if eo := f.hook(child, "p", hookpacks.EventSessionStart); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("child start of its absent parent: %+v", eo)
	}
	if eo := f.hook(f.as(t, "p", ""), "p", hookpacks.EventSessionStart); eo != nil {
		t.Fatal(eo)
	}
	equalIDs(t, "child list", f.listIDs(child, t, nil), "c")
	equalIDs(t, "parent list", f.listIDs(f.as(t, "p", ""), t, nil), "c", "p")
}

// TestCompletionCheckOwnerOnly: completion_check names a job no session owns
// yet, so a principal is refused before the handler runs.
func TestCompletionCheckOwnerOnly(t *testing.T) {
	f := newScopeFixture(t)
	reached := 0
	f.reg.Register(hookpacks.MethodCompletionCheck, func(context.Context, json.RawMessage) (any, error) {
		reached++
		return struct{}{}, nil
	})
	params := map[string]string{"session_id": "a", "job_id": "job-7", "event_type": "Stop"}
	if _, eo := f.call(f.as(t, "a", ""), hookpacks.MethodCompletionCheck, params); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("principal completion_check: %+v", eo)
	}
	if _, eo := f.call(owner(), hookpacks.MethodCompletionCheck, params); eo != nil || reached != 1 {
		t.Fatalf("owner completion_check: %+v, reached %d", eo, reached)
	}
}
