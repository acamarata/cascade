package conversation

// Purpose: the failure branches of the thread-scope code: a store that is
//   not the SQL store, a database that cannot be read, a malformed scope
//   member, a non-project principal. Each must refuse and write nothing.
// Inputs: real SQLite databases (one closed on purpose), the real adapter.
// Outputs: test verdicts only.
// SPORT: internal.conversation.adapter (scoped reads).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// foreignStore is a Store that is not the SQL conversation store.
type foreignStore struct{ Store }

func ownerCallerCtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

// closedScopeFixture is a fixture whose database is closed before any scope
// schema work, so every scope read or write meets a dead connection.
func closedScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := newScopeFixture(t, openTestDB(t))
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func wantKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("error = %v, want kind %v", err, kind)
	}
}

func TestScopeDBRefusesAForeignStore(t *testing.T) {
	_, err := scopeDB(context.Background(), foreignStore{}, newTestClock())
	if !errors.Is(err, errScopeStore) {
		t.Fatalf("scopeDB on a foreign store = %v, want errScopeStore", err)
	}
	a := NewAdapter(foreignStore{}, nil, nil, newTestClock(), ModeEmbedded)
	f := newScopeFixture(t, openTestDB(t))
	if err := a.attributeNew(f.principalCtx(t, "project:alpha"), "t-1"); !errors.Is(err, errScopeStore) {
		t.Fatalf("attributeNew for a principal on a foreign store = %v, want errScopeStore", err)
	}
	if err := a.attributeNew(ownerCallerCtx(), "t-1"); err != nil {
		t.Fatalf("the owner on a foreign store needs no attribution: %v", err)
	}
}

func TestAttributeNewRefusesANonProjectPrincipal(t *testing.T) {
	f := newScopeFixture(t, openTestDB(t))
	a := NewAdapter(f.store, nil, nil, newTestClock(), ModeEmbedded)
	elsewhere := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-elsewhere", Kind: principal.KindSession, Scope: "elsewhere"}})
	if err := a.attributeNew(elsewhere, "t-2"); !errors.Is(err, errScopeDenied) {
		t.Fatalf("attributeNew for a non-project principal = %v, want errScopeDenied", err)
	}
	if n, scope := f.state(t, "t-2"); n != 0 || scope != "" {
		t.Fatalf("refused attribution left state: %d turns, scope %q", n, scope)
	}
}

func TestScopeSchemaAndReadsFailClosedOnADeadDatabase(t *testing.T) {
	ctx := context.Background()
	f := closedScopeFixture(t)
	if err := ensureScopeSchema(ctx, f.db, newTestClock()); err == nil {
		t.Fatal("scope schema applied to a closed database")
	}
	_, err := scopedThreadIDs(ctx, f.db, "project:alpha")
	wantKind(t, err, cascade.KindUnavailable)
	if _, err := resolveView(f.principalCtx(t, "project:alpha"), f.store, newTestClock(), ""); err == nil {
		t.Fatal("a principal view resolved on a closed database")
	}
	a := NewAdapter(f.store, nil, nil, newTestClock(), ModeEmbedded)
	if err := a.attributeNew(f.principalCtx(t, "project:alpha"), "t-3"); err == nil {
		t.Fatal("attribution succeeded on a closed database")
	}
	if _, err := a.scopedAppendThread(f.principalCtx(t, "project:alpha"), appendTurnParams{ThreadID: "t-3"}); err == nil {
		t.Fatal("a principal continued a thread whose view could not be read")
	}
	if _, err := a.scopedAppendThread(ownerCallerCtx(), appendTurnParams{ThreadID: "t-3"}); err == nil {
		t.Fatal("the owner continued a thread on a closed database")
	}
}

func TestAttributeThreadInsertFailure(t *testing.T) {
	db := openTestDB(t)
	f := newScopeFixture(t, db)
	if err := ensureScopeSchema(context.Background(), f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP TABLE ` + tableThreadScope); err != nil {
		t.Fatal(err)
	}
	wantKind(t, attributeThread(tx, "t-4", "project:alpha"), cascade.KindUnavailable)
}

func TestScopedVerbsRefuseAMalformedScopeMember(t *testing.T) {
	f := newScopeFixture(t, openTestDB(t))
	ctx := ownerCallerCtx()
	for _, method := range []string{"chat.list_threads", "chat.get_thread"} {
		raw := json.RawMessage(`{"scope":5,"thread_id":"x"}`)
		_, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
		if eo == nil || eo.Code != cascade.KindInvalidInput.JSONRPCCode() {
			t.Fatalf("%s with a non-string scope = %+v, want invalid input", method, eo)
		}
	}
}
