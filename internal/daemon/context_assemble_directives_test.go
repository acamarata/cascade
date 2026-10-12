package daemon

// Purpose: covers context_assemble_directives.go, the supervision-directive
//   drain on context.slice: who may drain, which sessions drain, and the
//   one-time budget doubling a context directive grants.
// Constraints: a real git repository, a real modernc SQLite scope graph,
//   the real sessions and directive stores over an in-memory provider.Store,
//   driven through the REAL registry.Dispatch. HOME is a t.TempDir(); no
//   network. "Queue byte-identical" is a scan of every key and value in the
//   directive namespace before and after the request.

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// drainFixture is one registry with context.slice bound, plus the stores
// behind its drainer.
type drainFixture struct {
	dir        string
	reg        *rpc.Registry
	kv         provider.Store
	sessions   *sessions.Store
	directives *supervision.DirectiveStore
}

// newDrainFixture registers context.slice with opts (the live drainer when
// withDrainer is set) over a fresh repository and store.
func newDrainFixture(t *testing.T, withDrainer bool, extra ...ContextOption) *drainFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	seedRepo(t, dir)
	chdir(t, dir)
	paths := fakePaths{root: dir}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	f := &drainFixture{dir: dir, reg: rpc.NewRegistry(), kv: storetest.NewMemStore()}
	f.sessions = sessions.New(f.kv, clock, nil)
	f.directives = supervision.NewDirectiveStore(f.kv, clock)
	var opts []ContextOption
	if withDrainer {
		opts = append(opts, WithDirectiveDrain(NewLiveDirectiveDrainer(f.sessions, f.directives)))
	}
	db, err := RegisterContextAssembleHandler(f.reg, paths, clock, append(opts, extra...)...)
	if err != nil {
		t.Fatalf("RegisterContextAssembleHandler: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return f
}

// session stores id in state.
func (f *drainFixture) session(t *testing.T, id, state string) {
	t.Helper()
	if err := f.sessions.Upsert(context.Background(), sessions.SessionRecord{SessionID: id, State: state}); err != nil {
		t.Fatal(err)
	}
}

// enqueue queues one directive for id.
func (f *drainFixture) enqueue(t *testing.T, id string, kind supervision.DirectiveKind, text string, since int64) {
	t.Helper()
	d := supervision.Directive{SessionID: id, Kind: kind, Text: text, StalledSince: since}
	if err := f.directives.Enqueue(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

// snapshot renders every key and value in the directive namespace.
func (f *drainFixture) snapshot(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	it, err := f.kv.Scan(ctx, "supervision.directives", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	var b strings.Builder
	for it.Next(ctx) {
		b.WriteString(it.Key() + "=" + string(it.Value()) + "\n")
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// slice dispatches context.slice for sessionID under ctx.
func (f *drainFixture) slice(ctx context.Context, t *testing.T, sessionID string) ContextSliceResult {
	t.Helper()
	params, _ := json.Marshal(ContextAssembleParams{Cwd: f.dir, SessionID: sessionID})
	res, errObj := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: ContextSliceMethod, ID: []byte("1"), Params: params})
	if errObj != nil {
		t.Fatalf("context.slice: %+v", errObj)
	}
	out, ok := res.(ContextSliceResult)
	if !ok {
		t.Fatalf("context.slice result is %T", res)
	}
	return out
}

func ownerCLI() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func sessionPrincipal(id string) context.Context {
	p := principal.Principal{ID: "p-" + id, Kind: principal.KindSession, SessionID: id, Scope: "project/x"}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

// assertUntouched requires no directives and a byte-identical queue.
func assertUntouched(t *testing.T, f *drainFixture, got ContextSliceResult, before string) {
	t.Helper()
	if len(got.Directives) != 0 {
		t.Errorf("Directives = %q, want none", got.Directives)
	}
	if before == "" {
		t.Fatal("the queue was empty before the request; the check would prove nothing")
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("the queue changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestContextSliceDrainsSessionDirectives(t *testing.T) {
	f := newDrainFixture(t, true)
	f.session(t, "s1", "active")
	f.session(t, "s2", "stalled")
	f.enqueue(t, "s1", supervision.DirectiveRetry, "first", 1)
	f.enqueue(t, "s1", supervision.DirectiveRetry, "second", 2)
	f.enqueue(t, "s2", supervision.DirectiveRetry, "for s2", 3)

	if got := f.slice(ownerCLI(), t, "s1").Directives; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("owner CLI drain = %q, want [first second] in enqueue order", got)
	}
	if got := f.slice(ownerCLI(), t, "s1").Directives; len(got) != 0 {
		t.Fatalf("a second slice drained %q again", got)
	}
	if got := f.slice(sessionPrincipal("s2"), t, "s2").Directives; !reflect.DeepEqual(got, []string{"for s2"}) {
		t.Fatalf("the session's own principal drained %q, want [for s2]", got)
	}
}

func TestContextSliceContextDirectiveDoublesBudgetOnce(t *testing.T) {
	var budgets []int
	spy := func(o *contextOptions) {
		o.compute = func(_ context.Context, _ scope.ResolveDeps, p ContextAssembleParams) (ContextSliceResult, error) {
			budgets = append(budgets, *p.MaxTokens)
			return ContextSliceResult{}, nil
		}
	}
	f := newDrainFixture(t, true, spy)
	f.session(t, "s1", "active")
	f.enqueue(t, "s1", supervision.DirectiveContext, "boost one", 1)
	f.enqueue(t, "s1", supervision.DirectiveContext, "boost two", 2)
	budget := 300
	params, _ := json.Marshal(ContextAssembleParams{Cwd: f.dir, SessionID: "s1", MaxTokens: &budget})
	res, errObj := f.reg.Dispatch(ownerCLI(), &rpc.Request{JSONRPC: "2.0", Method: ContextSliceMethod, ID: []byte("1"), Params: params})
	if errObj != nil {
		t.Fatalf("context.slice: %+v", errObj)
	}
	if !reflect.DeepEqual(budgets, []int{300, 600}) {
		t.Fatalf("assembly budgets = %v, want [300 600]: two context directives double the budget once", budgets)
	}
	if got := res.(ContextSliceResult).Directives; len(got) != 2 {
		t.Fatalf("Directives = %q, want both context directives", got)
	}
	if got := doubledBudget(nil); got != 2*defaultContextMaxTokens {
		t.Errorf("doubledBudget(nil) = %d, want twice the default", got)
	}
}

func TestContextSliceUnknownSessionDrainsNothing(t *testing.T) {
	f := newDrainFixture(t, true)
	f.session(t, "gone", "closed")
	f.session(t, "odd", "bogus") // Upsert does not validate State: an unparsable record must fail closed
	f.enqueue(t, "ghost", supervision.DirectiveRetry, "never registered", 1)
	f.enqueue(t, "gone", supervision.DirectiveRetry, "closed already", 2)
	f.enqueue(t, "odd", supervision.DirectiveRetry, "unparsable state", 3)
	for _, id := range []string{"ghost", "gone", "odd"} {
		before := f.snapshot(t)
		assertUntouched(t, f, f.slice(ownerCLI(), t, id), before)
	}
}

func TestContextSliceForeignPrincipalDrainsNothing(t *testing.T) {
	f := newDrainFixture(t, true)
	f.session(t, "a", "active")
	f.session(t, "b", "active")
	f.enqueue(t, "b", supervision.DirectiveRetry, "for b only", 1)
	before := f.snapshot(t)
	assertUntouched(t, f, f.slice(sessionPrincipal("a"), t, "b"), before)
}

func TestContextSliceWithoutDrainerUnchanged(t *testing.T) {
	f := newDrainFixture(t, false)
	f.session(t, "s1", "active")
	f.enqueue(t, "s1", supervision.DirectiveRetry, "queued", 1)
	before := f.snapshot(t)
	assertUntouched(t, f, f.slice(ownerCLI(), t, "s1"), before)
}

func TestContextSliceAnonymousCallerDrainsNothing(t *testing.T) {
	f := newDrainFixture(t, true)
	f.session(t, "s1", "active")
	f.enqueue(t, "s1", supervision.DirectiveRetry, "queued", 1)
	before := f.snapshot(t)
	assertUntouched(t, f, f.slice(context.Background(), t, "s1"), before)
	ownerView := principal.WithCaller(context.Background(), principal.Caller{})
	assertUntouched(t, f, f.slice(ownerView, t, "s1"), before)
	assertUntouched(t, f, f.slice(ownerCLI(), t, ""), before)
}
