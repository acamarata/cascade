package conversation

// Purpose: chat.create_thread (adapter_createthread.go) driven through a real
//   *rpc.Registry.Dispatch with the access policy on, over a real sqlite
//   store: the id is minted daemon-side, the thread row, its marker and its
//   attribution commit in one transaction, a refused or failed request
//   writes nothing, and the attribution comes only from the Caller.
// SPORT: internal.conversation.adapter (chat.create_thread, tests).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// createVia dispatches chat.create_thread with raw params on ctx.
func (f *scopeFixture) createVia(ctx context.Context, raw string) (string, *rpc.ErrorObject) {
	var params json.RawMessage
	if raw != "" {
		params = json.RawMessage(raw)
	}
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: MethodCreateThread, Params: params})
	if eo != nil {
		return "", eo
	}
	return res.(createThreadResult).ThreadID, nil
}

// rowCount counts table's rows, with the thread_scope schema applied first
// so an empty count is never a missing table.
func (f *scopeFixture) rowCount(t *testing.T, table string) int {
	t.Helper()
	if err := ensureScopeSchema(context.Background(), f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// written is the total of the three tables create_thread writes.
func (f *scopeFixture) written(t *testing.T) int {
	t.Helper()
	return f.rowCount(t, tableThread) + f.rowCount(t, tableThreadPrivacy) + f.rowCount(t, tableThreadScope)
}

func newCreateFixture(t *testing.T) *scopeFixture {
	t.Helper()
	return newScopeFixture(t, openTestDB(t))
}

// TestCreateThreadWritesMarkerInSameTx: the RPC mints an id and the thread
// row, its marker and its attribution all exist afterwards; and the writes
// are ONE transaction, so a marker or attribution refusal after the thread
// row insert leaves no thread row. Failing input: committing the thread row
// in its own transaction leaves "th-atomic" behind.
func TestCreateThreadWritesMarkerInSameTx(t *testing.T) {
	f := newCreateFixture(t)
	ctx := context.Background()
	id, eo := f.createVia(ownerCallerCtx(), `{"privacy_mode":"local-only"}`)
	if eo != nil {
		t.Fatalf("chat.create_thread: %+v", eo)
	}
	if _, ok, err := f.store.GetThread(ctx, id); err != nil || !ok {
		t.Fatalf("GetThread(%s) ok=%v err=%v, want the thread row", id, ok, err)
	}
	if got, err := f.store.ThreadPrivacy(ctx, id); err != nil || got != provider.SensitivityLocalOnly || markerRows(t, f.store, id) != 1 {
		t.Fatalf("ThreadPrivacy(%s) = %s, %v; want one local-only marker", id, got, err)
	}
	if turns, scope := f.state(t, id); turns != 0 || scope != ownerScope {
		t.Fatalf("state = %d turns, scope %q; want 0 turns attributed %q", turns, scope, ownerScope)
	}

	if err := f.store.SetThreadPrivacy(ctx, "th-atomic", provider.SensitivityPublic); err != nil {
		t.Fatalf("seed conflicting marker: %v", err)
	}
	err := f.store.CreateThread(ctx, "th-atomic", provider.SensitivityLocalOnly, ownerScope, 1)
	if !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "write-once") {
		t.Fatalf("CreateThread over a conflicting marker = %v, want the write-once KindConflict", err)
	}
	if _, ok, err := f.store.GetThread(ctx, "th-atomic"); err != nil || ok {
		t.Fatalf("GetThread(th-atomic) ok=%v err=%v; the thread row survived its marker's refusal", ok, err)
	}
	if _, scope := f.state(t, "th-atomic"); scope != "" {
		t.Fatalf("th-atomic attributed %q after a refused create", scope)
	}

	if err := runTx(ctx, f.db, func(tx *sql.Tx) error { return attributeThread(tx, "th-attr", "project:a") }); err != nil {
		t.Fatalf("seed conflicting attribution: %v", err)
	}
	err = f.store.CreateThread(ctx, "th-attr", provider.SensitivityInternal, ownerScope, 1)
	if !isSentinel(err, errScopeConflict) {
		t.Fatalf("CreateThread over a conflicting attribution = %v, want %v", err, errScopeConflict)
	}
	if _, ok, _ := f.store.GetThread(ctx, "th-attr"); ok || markerRows(t, f.store, "th-attr") != 0 {
		t.Fatalf("th-attr thread=%v markers=%d; an attribution refusal must roll back the row and the marker",
			ok, markerRows(t, f.store, "th-attr"))
	}
}

// TestCreateThreadRollsBackOnMarkerFailure drops the marker table, so the
// marker insert fails after the thread row insert: no thread row and no
// attribution row survive. Failing input: a thread-row write outside the
// transaction leaves an unmarked thread.
func TestCreateThreadRollsBackOnMarkerFailure(t *testing.T) {
	f := newCreateFixture(t)
	if f.rowCount(t, tableThread) != 0 {
		t.Fatal("fresh fixture is not empty")
	}
	if _, err := f.db.Exec(`DROP TABLE ` + tableThreadPrivacy); err != nil {
		t.Fatalf("drop marker table: %v", err)
	}
	id, eo := f.createVia(ownerCallerCtx(), `{"privacy_mode":"internal"}`)
	if eo == nil || eo.Code != cascade.KindUnavailable.JSONRPCCode() {
		t.Fatalf("create_thread with no marker table = %q, %+v; want KindUnavailable", id, eo)
	}
	if n := f.rowCount(t, tableThread); n != 0 {
		t.Fatalf("thread rows = %d after a failed marker insert, want 0", n)
	}
	if n := f.rowCount(t, tableThreadScope); n != 0 {
		t.Fatalf("attribution rows = %d after a failed marker insert, want 0", n)
	}
}

// TestCreateThreadDuplicateIDConflict: creation never adopts an existing
// id. A second CreateThread of a created id, and a CreateThread of a thread
// an append already created, are errThreadExists and write nothing.
func TestCreateThreadDuplicateIDConflict(t *testing.T) {
	f := newCreateFixture(t)
	ctx := context.Background()
	if err := ensureScopeSchema(ctx, f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateThread(ctx, "th-dup", provider.SensitivityLocalOnly, ownerScope, 1); err != nil {
		t.Fatalf("first CreateThread: %v", err)
	}
	before := f.written(t)
	if err := f.store.CreateThread(ctx, "th-dup", provider.SensitivityPublic, "project:x", 2); !isSentinel(err, errThreadExists) {
		t.Fatalf("second CreateThread = %v, want %v", err, errThreadExists)
	}
	if got, _ := f.store.ThreadPrivacy(ctx, "th-dup"); got != provider.SensitivityLocalOnly {
		t.Fatalf("ThreadPrivacy = %s, want local-only (unchanged)", got)
	}
	if _, scope := f.state(t, "th-dup"); scope != ownerScope {
		t.Fatalf("th-dup attributed %q, want %q (unchanged)", scope, ownerScope)
	}
	seedThread(t, f.store, "th-appended")
	before++ // seedThread's own thread row
	if err := f.store.CreateThread(ctx, "th-appended", provider.SensitivityPublic, ownerScope, 3); !isSentinel(err, errThreadExists) {
		t.Fatalf("CreateThread over an appended thread = %v, want %v", err, errThreadExists)
	}
	if after := f.written(t); after != before {
		t.Fatalf("rows written = %d, want %d: a refused create wrote something", after, before)
	}
}

// TestCreateThreadRefusesUnknownTier: privacy_mode is REQUIRED and exact.
// Absent, empty, null, misspelled, differently cased, an extra field or
// trailing data is KindInvalidInput and nothing is written; the same
// fixture then accepts a valid mode, so the count is not vacuous.
func TestCreateThreadRefusesUnknownTier(t *testing.T) {
	f := newCreateFixture(t)
	cases := map[string]string{
		"no params": "", "empty object": `{}`, "empty mode": `{"privacy_mode":""}`,
		"null mode": `{"privacy_mode":null}`, "unknown tier": `{"privacy_mode":"super-secret"}`,
		"wrong case": `{"privacy_mode":"LOCAL-ONLY"}`, "underscore": `{"privacy_mode":"local_only"}`,
		"extra field": `{"privacy_mode":"public","thread_id":"caller-chosen-id"}`,
		"trailing":    `{"privacy_mode":"public"} {}`, "array": `["public"]`,
	}
	for name, raw := range cases {
		if _, eo := f.createVia(ownerCallerCtx(), raw); eo == nil || eo.Code != cascade.KindInvalidInput.JSONRPCCode() {
			t.Errorf("%s: errObj = %+v, want KindInvalidInput", name, eo)
		}
	}
	if n := f.written(t); n != 0 {
		t.Fatalf("refused create_thread calls wrote %d rows, want 0", n)
	}
	if _, eo := f.createVia(ownerCallerCtx(), `{"privacy_mode":"public"}`); eo != nil {
		t.Fatalf("valid create_thread: %+v", eo)
	}
	if n := f.written(t); n != 3 {
		t.Fatalf("one valid create wrote %d rows, want 3 (thread, marker, attribution)", n)
	}
}

// TestAdapterCreateThread_MintsDistinctIDs: two calls never collide and
// every id is a daemon-minted NewThreadID, never a caller-named one.
func TestAdapterCreateThread_MintsDistinctIDs(t *testing.T) {
	f := newCreateFixture(t)
	id1, eo1 := f.createVia(ownerCallerCtx(), `{"privacy_mode":"restricted"}`)
	id2, eo2 := f.createVia(ownerCallerCtx(), `{"privacy_mode":"restricted"}`)
	if eo1 != nil || eo2 != nil {
		t.Fatalf("create_thread errors: %+v, %+v", eo1, eo2)
	}
	if id1 == id2 || !strings.HasPrefix(id1, "thread-") || !strings.HasPrefix(id2, "thread-") {
		t.Fatalf("create_thread minted %q and %q, want two distinct NewThreadID values", id1, id2)
	}
	if n := f.rowCount(t, tableThread); n != 2 {
		t.Fatalf("thread rows = %d, want 2", n)
	}
}

// TestAdapterCreateThread_WiredOnRealRegistry: RegisterHandlers binds the
// verb on the same registry as every other chat.* method, and with the
// daemon's access policy on it dispatches (an unregistered or undeclared
// method would be refused before the handler).
func TestAdapterCreateThread_WiredOnRealRegistry(t *testing.T) {
	f := newCreateFixture(t)
	if !f.reg.Registered(MethodCreateThread) || !f.reg.Registered(MethodAppendTurn) {
		t.Fatalf("registry lacks %s or %s", MethodCreateThread, MethodAppendTurn)
	}
	if _, eo := f.createVia(ownerCallerCtx(), `{"privacy_mode":"internal"}`); eo != nil {
		t.Fatalf("create_thread through the access-checked registry: %+v", eo)
	}
}

// TestCreateThreadAttributionFromCaller: the owner CLI's thread is
// attributed "owner"; a principal's thread is attributed to its own project
// scope and is in its view; a principal without a project scope is refused
// before anything is written.
func TestCreateThreadAttributionFromCaller(t *testing.T) {
	f := newCreateFixture(t)
	owned, eo := f.createVia(ownerCallerCtx(), `{"privacy_mode":"internal"}`)
	if eo != nil {
		t.Fatalf("owner create: %+v", eo)
	}
	pctx := f.principalCtx(t, "project:a")
	mine, eo := f.createVia(pctx, `{"privacy_mode":"internal"}`)
	if eo != nil {
		t.Fatalf("principal create: %+v", eo)
	}
	if _, scope := f.state(t, owned); scope != ownerScope {
		t.Fatalf("owner thread attributed %q, want %q", scope, ownerScope)
	}
	if _, scope := f.state(t, mine); scope != "project:a" {
		t.Fatalf("principal thread attributed %q, want project:a", scope)
	}
	view, err := CallerThreadView(pctx, f.store, newTestClock(), "")
	if err != nil || !view(mine) || view(owned) {
		t.Fatalf("principal view: own=%v owner=%v err=%v; want only its own thread", view(mine), view(owned), err)
	}
	before := f.written(t)
	if _, eo := f.createVia(principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{
		ID: "p-personal", Kind: principal.KindSession, Scope: "personal"}}), `{"privacy_mode":"internal"}`); !refusedDenied(eo) {
		t.Fatalf("non-project principal create = %+v, want permission-denied", eo)
	}
	if after := f.written(t); after != before {
		t.Fatalf("a refused principal create wrote %d rows", after-before)
	}
}

// TestCreateThreadRefusesAnonymous: a call with no Caller is refused by the
// access-checked registry, and by the handler itself on a registry with no
// access policy (as is a Caller that is neither owner nor principal);
// nothing is written either way.
func TestCreateThreadRefusesAnonymous(t *testing.T) {
	f := newCreateFixture(t)
	if _, eo := f.createVia(context.Background(), `{"privacy_mode":"public"}`); !refusedDenied(eo) {
		t.Fatalf("anonymous create on the access-checked registry = %+v, want permission-denied", eo)
	}
	bare := rpc.NewRegistry()
	NewAdapter(f.store, nil, nil, newTestClock(), ModeEmbedded).RegisterHandlers(bare)
	unchecked := &scopeFixture{reg: bare, store: f.store, db: f.db}
	for name, ctx := range map[string]context.Context{
		"no caller":    context.Background(),
		"empty caller": principal.WithCaller(context.Background(), principal.Caller{}),
	} {
		_, eo := unchecked.createVia(ctx, `{"privacy_mode":"public"}`)
		if !refusedDenied(eo) || eo.Message != errCreateNoCaller.Error() {
			t.Fatalf("%s on an unchecked registry = %+v, want %v", name, eo, errCreateNoCaller)
		}
	}
	if n := f.written(t); n != 0 {
		t.Fatalf("anonymous creates wrote %d rows, want 0", n)
	}
}
