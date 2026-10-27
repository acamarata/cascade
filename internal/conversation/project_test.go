package conversation

// Purpose: thread project (project.go) over a real sqlite store and a real
//   *rpc.Registry: the project is recorded once at creation, from the Caller
//   and cwd, in the creating transaction; a failure of any kind leaves the
//   thread unscoped (never widened); and SearchTurns narrows by it in SQL.
// SPORT: internal.conversation.project/ADDED (tests) (P1-CPA-06).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// projectFixture is a scopeFixture whose adapter resolves the fake cwds
// below; calls records every cwd the resolver was asked about.
type projectFixture struct {
	*scopeFixture
	adapter *Adapter
	calls   []string
	err     error
}

const (
	cwdA = "/fake/workspace/project-a"
	cwdB = "/fake/workspace/project-b"
)

func newProjectFixture(t *testing.T) *projectFixture {
	t.Helper()
	f := &projectFixture{scopeFixture: newScopeFixture(t, openTestDB(t))}
	a := NewAdapter(f.store, nil, nil, newTestClock(), ModeEmbedded)
	f.adapter = a
	a.SetProjectResolver(func(_ context.Context, in scope.ResolveInput) (string, error) {
		f.calls = append(f.calls, in.Cwd)
		switch {
		case f.err != nil:
			return "", f.err
		case in.Cwd == cwdA:
			return "project:a", nil
		case in.Cwd == cwdB:
			return "project:b", nil
		}
		return "", nil // any other cwd: the general bucket
	})
	f.reg = rpc.NewRegistry()
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, newTestClock().Now()) })
	a.RegisterHandlers(f.reg)
	return f
}

// project reads threadID's recorded project through the store.
func (f *projectFixture) project(t *testing.T, threadID string) (string, bool) {
	t.Helper()
	ref, ok, err := f.store.ThreadProject(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ThreadProject(%s): %v", threadID, err)
	}
	return ref, ok
}

func (f *projectFixture) rows(t *testing.T) int { return f.rowCount(t, tableThreadProject) }

// create dispatches chat.create_thread carrying cwd.
func (f *projectFixture) create(ctx context.Context, cwd string) (string, *rpc.ErrorObject) {
	return f.createVia(ctx, fmt.Sprintf(`{"privacy_mode":"internal","cwd":%q}`, cwd))
}

// appendCwd dispatches chat.append_turn (empty threadID creates) carrying cwd.
func (f *projectFixture) appendCwd(ctx context.Context, threadID, cwd string) (string, *rpc.ErrorObject) {
	params := map[string]any{"role": "user", "cwd": cwd, "segments": []map[string]string{{"kind": "text", "content": "hello"}}}
	if threadID != "" {
		params["thread_id"] = threadID
	}
	raw, _ := json.Marshal(params)
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: MethodAppendTurn, Params: raw})
	if eo != nil {
		return "", eo
	}
	return res.(appendTurnResult).ThreadID, nil
}

// TestThread_ScopeRefSetOnceAtCreation: SetThreadProject is insert-once, and
// a continuation from another project never changes (or adds) a row.
// Failing input: an upsert, or a continuation that records its own cwd.
func TestThread_ScopeRefSetOnceAtCreation(t *testing.T) {
	f := newProjectFixture(t)
	ctx := context.Background()
	if err := f.store.SetThreadProject(ctx, "th-once", "project:a"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetThreadProject(ctx, "th-once", "project:b"); err != nil {
		t.Fatalf("second SetThreadProject: %v, want a no-op", err)
	}
	if ref, ok := f.project(t, "th-once"); !ok || ref != "project:a" || f.rows(t) != 1 {
		t.Fatalf("th-once = %q ok=%v rows=%d; want the first write only", ref, ok, f.rows(t))
	}

	id, eo := f.create(ownerCallerCtx(), cwdA)
	if eo != nil {
		t.Fatal(eo)
	}
	if _, eo := f.appendCwd(ownerCallerCtx(), id, cwdB); eo != nil {
		t.Fatalf("continuation from project b: %+v", eo)
	}
	if ref, _ := f.project(t, id); ref != "project:a" || f.rows(t) != 2 {
		t.Fatalf("thread project after a b-cwd append = %q, rows %d; want project:a, 2", ref, f.rows(t))
	}
	// A thread created unscoped is not scoped by a later append either.
	bare, _ := f.appendCwd(ownerCallerCtx(), "", "")
	if _, eo := f.appendCwd(ownerCallerCtx(), bare, cwdA); eo != nil {
		t.Fatal(eo)
	}
	if _, ok := f.project(t, bare); ok {
		t.Fatal("an append to an existing thread added a project row")
	}
}

// TestCreateThread_RecordsScopeFromCwd: both creating paths resolve the
// owner's cwd and record the result; a resolution to no project is the
// general bucket (row ""), distinct from no row.
func TestCreateThread_RecordsScopeFromCwd(t *testing.T) {
	f := newProjectFixture(t)
	viaCreate, _ := f.create(ownerCallerCtx(), cwdA)
	viaAppend, _ := f.appendCwd(ownerCallerCtx(), "", cwdB)
	general, _ := f.create(ownerCallerCtx(), "/fake/elsewhere")
	for id, want := range map[string]string{viaCreate: "project:a", viaAppend: "project:b", general: ""} {
		if ref, ok := f.project(t, id); !ok || ref != want {
			t.Fatalf("thread %s project = %q ok=%v, want %q recorded", id, ref, ok, want)
		}
	}
	if len(f.calls) != 3 {
		t.Fatalf("resolver calls = %v, want one per creating call", f.calls)
	}
}

// TestThread_NoScopeRowOnResolverError: a resolver error, a nil resolver, an
// empty cwd, and an in-process call with no Caller all create the thread with
// NO row. Failing input: a fallback to the general bucket on error.
func TestThread_NoScopeRowOnResolverError(t *testing.T) {
	f := newProjectFixture(t)
	f.err = errors.New("fake resolver failure")
	failed, eo := f.create(ownerCallerCtx(), cwdA)
	if eo != nil {
		t.Fatalf("create with a failing resolver = %+v, want the thread created", eo)
	}
	f.err = nil
	empty, _ := f.create(ownerCallerCtx(), "")
	noCaller := f.appendNoCaller(t, cwdA)
	bare := newScopeFixture(t, openTestDB(t)) // adapter without any resolver
	nilResolver, _ := bare.createVia(ownerCallerCtx(), `{"privacy_mode":"internal","cwd":"`+cwdA+`"}`)
	for name, id := range map[string]string{"error": failed, "empty cwd": empty, "no caller": noCaller} {
		if id == "" {
			t.Fatalf("%s: no thread created", name)
		}
		if ref, ok := f.project(t, id); ok {
			t.Fatalf("%s: thread %s got project row %q, want none", name, id, ref)
		}
	}
	if _, ok, _ := bare.store.ThreadProject(context.Background(), nilResolver); ok || nilResolver == "" {
		t.Fatalf("nil resolver: thread %q scoped=%v, want created and unscoped", nilResolver, ok)
	}
	if len(f.calls) != 1 {
		t.Fatalf("resolver calls = %v; an empty cwd or no caller must not reach it", f.calls)
	}
}

// appendNoCaller drives the adapter handler directly with no Caller on ctx
// (the registry refuses such a call first; an in-process caller does not).
func (f *projectFixture) appendNoCaller(t *testing.T, cwd string) string {
	t.Helper()
	raw := fmt.Sprintf(`{"role":"user","cwd":%q,"segments":[{"kind":"text","content":"hi"}]}`, cwd)
	res, err := f.adapter.handleAppendTurn(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("append with no caller: %v", err)
	}
	return res.(appendTurnResult).ThreadID
}

// dropProjectTable drops the project table so the in-transaction write fails.
func (f *projectFixture) dropProjectTable(t *testing.T) {
	t.Helper()
	if _, err := f.db.Exec(`DROP TABLE ` + tableThreadProject); err != nil {
		t.Fatal(err)
	}
}

// TestAppendTurn_ScopeWriteFailureRefusesAppend: a failing project write
// refuses the creating append (KindUnavailable) and leaves no thread, marker
// or turn. Failing input: writing the project after the turn committed.
func TestAppendTurn_ScopeWriteFailureRefusesAppend(t *testing.T) {
	f := newProjectFixture(t)
	f.dropProjectTable(t)
	id, eo := f.appendCwd(ownerCallerCtx(), "", cwdA)
	if eo == nil || eo.Code != cascade.KindUnavailable.JSONRPCCode() {
		t.Fatalf("append with a failing project write = %q, %+v; want KindUnavailable", id, eo)
	}
	for _, table := range []string{tableThread, tableTurn, tableSegment, tableThreadPrivacy} {
		if n := f.rowCount(t, table); n != 0 {
			t.Fatalf("%s rows = %d after a refused append, want 0", table, n)
		}
	}
}

// TestCreateThread_ScopeWriteFailureRefuses is the create_thread twin.
func TestCreateThread_ScopeWriteFailureRefuses(t *testing.T) {
	f := newProjectFixture(t)
	f.dropProjectTable(t)
	id, eo := f.create(ownerCallerCtx(), cwdA)
	if eo == nil || eo.Code != cascade.KindUnavailable.JSONRPCCode() {
		t.Fatalf("create_thread with a failing project write = %q, %+v; want KindUnavailable", id, eo)
	}
	if n := f.written(t); n != 0 {
		t.Fatalf("%d rows written by a refused create_thread, want 0", n)
	}
}

// TestProjectFromPrincipalIgnoresCwd: a principal's project is its
// authenticated Scope; the cwd it sends is never resolved. Failing input:
// using the cwd (or letting it override) for a principal.
func TestProjectFromPrincipalIgnoresCwd(t *testing.T) {
	f := newProjectFixture(t)
	pctx := f.principalCtx(t, "project:p")
	viaCreate, eo := f.create(pctx, cwdA)
	if eo != nil {
		t.Fatal(eo)
	}
	viaAppend, eo := f.appendCwd(pctx, "", cwdB)
	if eo != nil {
		t.Fatal(eo)
	}
	for _, id := range []string{viaCreate, viaAppend} {
		if ref, ok := f.project(t, id); !ok || ref != "project:p" {
			t.Fatalf("principal thread %s project = %q ok=%v, want its Scope project:p", id, ref, ok)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("resolver consulted %v for a principal", f.calls)
	}
}

// TestProjectFor_FailsClosedForUnusableCallers: only a project-scoped principal
// or the owner CLI yields a project. A principal whose Scope is not a project
// scope gets nil even with a cwd the resolver would accept (and the resolver is
// not consulted); so does a Caller that is neither. Failing input: dropping the
// projectScope check records the principal's non-project Scope as its project.
func TestProjectFor_FailsClosedForUnusableCallers(t *testing.T) {
	f := newProjectFixture(t)
	for name, c := range map[string]principal.Caller{
		"principal with owner scope":  {Principal: &principal.Principal{ID: "p1", Scope: "owner"}},
		"principal with empty scope":  {Principal: &principal.Principal{ID: "p2"}},
		"principal with bare project": {Principal: &principal.Principal{ID: "p3", Scope: "project:"}},
		"neither owner nor principal": {},
	} {
		ctx := principal.WithCaller(context.Background(), c)
		if got := f.adapter.projectFor(ctx, cwdA); got != nil {
			t.Fatalf("%s: projectFor = %q, want nil", name, *got)
		}
	}
	ok := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p4", Scope: "project:x"}})
	if got := f.adapter.projectFor(ok, cwdA); got == nil || *got != "project:x" {
		t.Fatalf("project-scoped principal: projectFor = %v, want project:x (the fixture is not vacuous)", got)
	}
	if len(f.calls) != 0 {
		t.Fatalf("resolver consulted for %v", f.calls)
	}
}
