package supervision_test

// Purpose: a caller principal lists, reads and acknowledges only attention
// items whose source session lies in its own session subtree.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

type attnFixture struct {
	reg   *rpc.Registry
	sess  *sessions.Store
	store *supervision.Store
	items map[string]supervision.AttentionItem
}

func newAttnFixture(t *testing.T, withScope bool) *attnFixture {
	t.Helper()
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	kv := storetest.NewMemStore()
	sess := sessions.New(kv, clock, nil)
	parent := "a"
	for _, rec := range []sessions.SessionRecord{{SessionID: "a"}, {SessionID: "a1", ParentSessionID: &parent}, {SessionID: "b"}} {
		if err := sess.Upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	store := supervision.NewStore(kv, clock, nil, func() string { n++; return fmt.Sprintf("item-%d", n) }, 50)
	f := &attnFixture{reg: rpc.NewRegistry(), sess: sess, store: store, items: map[string]supervision.AttentionItem{}}
	for _, src := range []string{"a1", "b", "job-7"} {
		item, err := store.Push(ctx, supervision.AttentionItem{Kind: supervision.KindError, SourceRef: src,
			ScopeRef: scope.Ref{Kind: scope.ScopeKindProject, ID: "demo"}})
		if err != nil {
			t.Fatal(err)
		}
		f.items[src] = item
	}
	f.reg.SetAccessPolicy(nil)
	supervision.RegisterHandlers(f.reg, store, nil)
	if withScope {
		f.reg.Use(sessions.ScopeMiddleware(sess, sessions.SessionBinding{Methods: []string{hookpacks.MethodHookEvent},
			Creator: hookpacks.MethodHookEvent, StartEvents: []string{string(hookpacks.EventSessionStart)}}))
	}
	return f
}

func principalA() context.Context {
	p := principal.Principal{ID: "p-a", Kind: principal.KindSession, SessionID: "a", Scope: "project:demo"}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func (f *attnFixture) call(ctx context.Context, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	raw, _ := json.Marshal(params)
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		return nil, eo
	}
	out, _ := json.Marshal(res)
	return out, nil
}

func (f *attnFixture) sources(ctx context.Context, t *testing.T) []string {
	t.Helper()
	out, eo := f.call(ctx, supervision.MethodList, map[string]any{"own_scope": scope.Ref{Kind: scope.ScopeKindProject, ID: "demo"}})
	if eo != nil {
		t.Fatal(eo)
	}
	var res struct {
		Items []supervision.AttentionItem `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	var src []string
	for _, it := range res.Items {
		src = append(src, it.SourceRef)
	}
	return src
}

func TestPrincipalAckForeignAttentionRefused(t *testing.T) {
	f := newAttnFixture(t, true)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if got := f.sources(owner, t); len(got) != 3 {
		t.Fatalf("owner list = %v, want all three items", got)
	}
	if got := fmt.Sprint(f.sources(principalA(), t)); got != "[a1]" {
		t.Fatalf("principal list = %s, want [a1]", got)
	}
	var refusals []string
	for _, id := range []string{f.items["b"].ID, f.items["job-7"].ID, "item-404"} {
		for _, m := range []string{supervision.MethodGet, supervision.MethodAck} {
			_, eo := f.call(principalA(), m, map[string]string{"id": id})
			if eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
				t.Fatalf("%s %s: %+v, want permission denied", m, id, eo)
			}
			refusals = append(refusals, eo.Message)
		}
	}
	for _, m := range refusals {
		if m != refusals[0] {
			t.Errorf("refusals differ (existence oracle): %q vs %q", m, refusals[0])
		}
	}
	for _, src := range []string{"b", "job-7"} {
		item, err := f.store.Get(context.Background(), f.items[src].ID)
		if err != nil || item.AckedAt != nil {
			t.Errorf("refused ack changed %s: %+v %v", src, item, err)
		}
	}
	if _, eo := f.call(principalA(), supervision.MethodGet, map[string]string{"id": f.items["a1"].ID}); eo != nil {
		t.Fatal(eo)
	}
	if _, eo := f.call(principalA(), supervision.MethodAck, map[string]string{"id": f.items["a1"].ID}); eo != nil {
		t.Fatal(eo)
	}
	if item, err := f.store.Get(context.Background(), f.items["a1"].ID); err != nil || item.AckedAt == nil {
		t.Errorf("own ack did not land: %+v %v", item, err)
	}
}

// TestPrincipalAttentionWithoutPredicateSeesNothing: a principal dispatch
// with no session predicate installed fails closed.
func TestPrincipalAttentionWithoutPredicateSeesNothing(t *testing.T) {
	f := newAttnFixture(t, false)
	if got := f.sources(principalA(), t); len(got) != 0 {
		t.Fatalf("principal without predicate saw %v", got)
	}
	if _, eo := f.call(principalA(), supervision.MethodAck, map[string]string{"id": f.items["a1"].ID}); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("ack without predicate: %+v", eo)
	}
}

// TestPrincipalCannotClaimAttentionSource: a principal cannot start a
// "session" named after another entity's attention source to adopt its items.
func TestPrincipalCannotClaimAttentionSource(t *testing.T) {
	f := newAttnFixture(t, true)
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	if err := hookpacks.RegisterHookEventHandler(f.reg, f.sess, nil, clock); err != nil {
		t.Fatal(err)
	}
	_, eo := f.call(principalA(), hookpacks.MethodHookEvent, map[string]any{"harness": "claude-code",
		"event_type": string(hookpacks.EventSessionStart), "session_id": "job-7", "pid": 1, "account": "x",
		"timestamp_ms": clock.Now().UnixMilli()})
	if eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("principal start of a foreign id: %+v", eo)
	}
	if got := fmt.Sprint(f.sources(principalA(), t)); got != "[a1]" {
		t.Fatalf("principal list after claim attempt = %s, want [a1]", got)
	}
}

// TestSessionVisibilityLoopsAndDepth: the session predicate attention
// filtering relies on admits descendants at any depth and trusts no looping
// ancestry, even one that passes through the caller's own session.
func TestSessionVisibilityLoopsAndDepth(t *testing.T) {
	ctx := context.Background()
	sess := sessions.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), nil)
	link := func(id, parent string) {
		rec := sessions.SessionRecord{SessionID: id}
		if parent != "" {
			rec.ParentSessionID = &parent
		}
		if err := sess.Upsert(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	link("root", "")
	for i := 0; i < 100; i++ {
		link(fmt.Sprintf("d%d", i+1), fmt.Sprintf("d%d", i))
	}
	link("d0", "root")
	link("x", "y")
	link("y", "x")
	link("a", "a1")
	link("a1", "a")
	if !sess.Visibility(principal.Principal{SessionID: "root"})("d100", "") {
		t.Error("descendant at depth 101 hidden")
	}
	if sess.Visibility(principal.Principal{SessionID: "x"})("y", "") || sess.Visibility(principal.Principal{SessionID: "a1"})("a", "") {
		t.Error("looping ancestry admitted")
	}
}

// TestStartNeverLinksACycle: a principal whose minted parent is its own
// descendant starts its session without closing a parent loop.
func TestStartNeverLinksACycle(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	sess := sessions.New(storetest.NewMemStore(), clock, nil)
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	if err := hookpacks.RegisterHookEventHandler(reg, sess, nil, clock); err != nil {
		t.Fatal(err)
	}
	reg.Use(sessions.ScopeMiddleware(sess, sessions.SessionBinding{Methods: []string{hookpacks.MethodHookEvent},
		Creator: hookpacks.MethodHookEvent, StartEvents: []string{string(hookpacks.EventSessionStart)}}))
	for _, s := range [][2]string{{"c", "p"}, {"g", "c"}, {"p", "g"}} {
		pr := principal.Principal{ID: "p-" + s[0], Kind: principal.KindSession, SessionID: s[0], ParentSessionID: s[1], Scope: "project:demo"}
		raw, _ := json.Marshal(map[string]any{"harness": "claude-code", "event_type": string(hookpacks.EventSessionStart),
			"session_id": s[0], "pid": 1, "account": "x", "timestamp_ms": clock.Now().UnixMilli()})
		req := &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: hookpacks.MethodHookEvent, Params: raw}
		if _, eo := reg.Dispatch(principal.WithCaller(ctx, principal.Caller{Principal: &pr}), req); eo != nil {
			t.Fatal(s, eo)
		}
	}
	if rec, err := sess.Get(ctx, "p"); err != nil || rec.ParentSessionID != nil {
		t.Fatalf("p linked under its own descendant: %v %v", rec.ParentSessionID, err)
	}
}
