package supervision

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this test

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedMigrateClock is a fixed migrate.Clock for ApplyScopeSchema, never
// a bare time.Now.
type fixedMigrateClock struct{ t time.Time }

func (c fixedMigrateClock) Now() time.Time { return c.t }

// newTestGraphStore opens a fresh in-memory scope graph for one test.
func newTestGraphStore(t *testing.T) *scope.GraphStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clk := fixedMigrateClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatalf("ApplyScopeSchema: %v", err)
	}
	return scope.NewGraphStore(db)
}

// TestAttentionTraversalVisibility asserts list/get visibility resolves
// through the E/S-08.T4 closed traversal table (R-21.157(a)): a session
// scope with a declared depends_on edge to a project scope sees both its
// own chain and the route target.
func TestAttentionTraversalVisibility(t *testing.T) {
	ctx := context.Background()
	gs := newTestGraphStore(t)

	sess := scope.Ref{Kind: scope.ScopeKindSession, ID: "sess-1"}
	proj := scope.Ref{Kind: scope.ScopeKindProject, ID: "proj-1"}
	for _, ref := range []scope.Ref{sess, proj} {
		if err := gs.PutScope(ctx, scope.GraphRecord{Ref: ref}); err != nil {
			t.Fatalf("PutScope(%+v): %v", ref, err)
		}
	}
	if err := gs.PutEdge(ctx, scope.Edge{From: sess, To: proj, Kind: scope.EdgeKindDependsOn}); err != nil {
		t.Fatalf("PutEdge: %v", err)
	}

	visible, err := ResolveVisibleScopes(ctx, gs, []scope.Ref{sess}, sess)
	if err != nil {
		t.Fatalf("ResolveVisibleScopes: %v", err)
	}
	if !containsRef(visible, sess) || !containsRef(visible, proj) {
		t.Errorf("visible = %+v, want it to contain both sess and the depends_on target proj", visible)
	}
}

// TestAttentionAllBounded asserts --all expands to EXACTLY the
// traversal-table-visible scopes and no further: a foreign scope with NO
// declared edge from the caller's chain must not appear, and its items
// must not leak into the --all list.
func TestAttentionAllBounded(t *testing.T) {
	ctx := context.Background()
	gs := newTestGraphStore(t)

	sess := scope.Ref{Kind: scope.ScopeKindSession, ID: "sess-1"}
	foreign := scope.Ref{Kind: scope.ScopeKindProject, ID: "foreign-proj"}
	for _, ref := range []scope.Ref{sess, foreign} {
		if err := gs.PutScope(ctx, scope.GraphRecord{Ref: ref}); err != nil {
			t.Fatalf("PutScope(%+v): %v", ref, err)
		}
	}
	// Deliberately NO edge from sess to foreign.

	visible, err := ResolveVisibleScopes(ctx, gs, []scope.Ref{sess}, sess)
	if err != nil {
		t.Fatalf("ResolveVisibleScopes: %v", err)
	}
	if containsRef(visible, foreign) {
		t.Fatalf("visible = %+v, want it to NOT contain the un-edged foreign scope", visible)
	}

	// End-to-end: an item filed under the foreign scope must not appear
	// in a --all list built from this visible set.
	kv := storetest.NewMemStore()
	store := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	if _, err := store.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "leak-attempt", ScopeRef: foreign}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	items, err := store.ListInScopes(ctx, visible, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	for _, it := range items {
		if it.SourceRef == "leak-attempt" {
			t.Fatalf("foreign-scope item leaked into --all: %+v", it)
		}
	}
}

// containsRef reports whether refs contains target.
func containsRef(refs []scope.Ref, target scope.Ref) bool {
	for _, r := range refs {
		if r == target {
			return true
		}
	}
	return false
}

// fakeGrantChecker is a deterministic GrantChecker for tests: it grants
// exactly the capabilities named in granted.
type fakeGrantChecker struct {
	granted map[string]bool
	err     error
}

func (f fakeGrantChecker) Check(_ context.Context, req GrantCheckRequest) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.granted[req.Capability], nil
}

// TestAttentionCrossScopeSendRefused asserts every R-21.157(b) refusal
// leg: missing inbox.send, missing inbox.cross_scope_send, and (via a
// nil checker) no evaluator configured at all — each REFUSES with a
// typed permission error and queues nothing.
func TestAttentionCrossScopeSendRefused(t *testing.T) {
	ctx := context.Background()
	origin := sessionScope("origin")
	target := sessionScope("target")
	baseReq := PushRequest{
		Item:   AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: target},
		Origin: origin,
	}

	cases := []struct {
		name    string
		checker GrantChecker
	}{
		{"no evaluator configured", nil},
		{"missing inbox.send", fakeGrantChecker{granted: map[string]bool{}}},
		{"missing inbox.cross_scope_send", fakeGrantChecker{granted: map[string]bool{capabilityInboxSend: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kv := storetest.NewMemStore()
			store := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
			_, err := RoutePush(ctx, store, tc.checker, nil, baseReq)
			if !cascade.HasKind(err, cascade.KindPermissionDenied) {
				t.Fatalf("RoutePush err = %v, want KindPermissionDenied", err)
			}
			count, cerr := store.countAll(ctx)
			if cerr != nil {
				t.Fatalf("countAll: %v", cerr)
			}
			if count != 0 {
				t.Errorf("countAll = %d, want 0 (refused push must queue nothing)", count)
			}
		})
	}
}

// TestAttentionCrossScopeSendAllowed is the positive counterpart: both
// capabilities granted succeeds and the same-scope (non-addressed) path
// needs neither.
func TestAttentionCrossScopeSendAllowed(t *testing.T) {
	ctx := context.Background()
	origin := sessionScope("origin")
	target := sessionScope("target")
	kv := storetest.NewMemStore()
	store := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	checker := fakeGrantChecker{granted: map[string]bool{capabilityInboxSend: true, capabilityInboxCrossScopeSend: true}}

	_, err := RoutePush(ctx, store, checker, nil, PushRequest{
		Item:   AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: target},
		Origin: origin,
	})
	if err != nil {
		t.Fatalf("RoutePush with both capabilities granted: %v", err)
	}
}

func TestAttentionSameScopePushNeedsNoCapability(t *testing.T) {
	ctx := context.Background()
	origin := sessionScope("origin")
	kv := storetest.NewMemStore()
	store := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)

	_, err := RoutePush(ctx, store, nil, nil, PushRequest{
		Item:   AttentionItem{Kind: KindStall, SourceRef: "x", ScopeRef: origin},
		Origin: origin,
	})
	if err != nil {
		t.Fatalf("same-scope RoutePush with nil checker: %v", err)
	}
}

// TestAttentionGlobalKindSet asserts a global-scope item is limited to
// the closed kind set with the minimal schema, and an out-of-set kind
// refuses.
func TestAttentionGlobalKindSet(t *testing.T) {
	ctx := context.Background()
	origin := sessionScope("origin")
	global := ScopeRef{Kind: scope.ScopeKindGlobal, ID: "global"}
	checker := fakeGrantChecker{granted: map[string]bool{capabilityInboxSend: true, capabilityInboxCrossScopeSend: true}}

	// Every member of Kind is currently in the closed global set (see
	// attention.go's globalKindSet), so all four must succeed.
	for _, k := range []Kind{KindStall, KindElevationRefused, KindPolicyAsk, KindError} {
		kv := storetest.NewMemStore()
		store := NewStore(kv, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
		_, err := RoutePush(ctx, store, checker, nil, PushRequest{
			Item:   AttentionItem{Kind: k, SourceRef: "x", ScopeRef: global},
			Origin: origin,
		})
		if err != nil {
			t.Errorf("global push of closed-set kind %q refused: %v", k, err)
		}
	}
}

// TestAttentionGlobalKindSetOutOfSetRefuses asserts an out-of-set kind
// value refuses even though Validate would already reject it earlier in
// a real Push — this test exercises checkGlobalShape directly via a
// value that bypasses Kind.Valid to prove RoutePush's own gate, not just
// Validate's.
func TestAttentionGlobalKindSetOutOfSetRefuses(t *testing.T) {
	if err := checkGlobalShape(AttentionItem{Kind: "not-in-globalKindSet"}); err == nil {
		t.Fatal("checkGlobalShape on an out-of-set kind = nil, want a refusal")
	}
	if !cascade.HasKind(checkGlobalShape(AttentionItem{Kind: "not-in-globalKindSet"}), cascade.KindPermissionDenied) {
		t.Error("checkGlobalShape refusal is not KindPermissionDenied")
	}
}

func TestResolveVisibleScopesNilStoreFallsBackToOwnScope(t *testing.T) {
	ctx := context.Background()
	own := sessionScope("own")
	got, err := ResolveVisibleScopes(ctx, nil, []scope.Ref{own}, own)
	if err != nil {
		t.Fatalf("ResolveVisibleScopes(nil store): %v", err)
	}
	if len(got) != 1 || got[0] != own {
		t.Errorf("got = %+v, want [own]", got)
	}
}
