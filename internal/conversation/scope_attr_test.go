package conversation

// Purpose: the thread_scope migration on every backend, the create-only
//   attribution write, and the append guard: a principal never claims a
//   pre-migration thread and never writes into another scope's thread.
// Inputs: real SQLite databases (file and in-memory), the real adapter on an
//   rpc.Registry with the access policy on, principals minted by
//   internal/principal; postgres via scope_attr_postgres_test.go's tag.
// Outputs: test verdicts only.
// SPORT: internal.conversation.adapter (scoped reads).

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// postgresScopeLeg is set by the postgres-tagged file; nil without the tag.
var postgresScopeLeg func(t *testing.T)

type scopeFixture struct {
	reg    *rpc.Registry
	store  Store
	db     *sql.DB
	minter *principal.Minter
}

func newScopeFixture(t *testing.T, db *sql.DB) *scopeFixture {
	t.Helper()
	clock := newTestClock()
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	f := &scopeFixture{reg: rpc.NewRegistry(), store: NewStore(db), db: db, minter: principal.NewMinter(clock)}
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, clock.Now()) })
	NewAdapter(f.store, nil, nil, clock, ModeEmbedded).RegisterHandlers(f.reg)
	return f
}

func (f *scopeFixture) principalCtx(t *testing.T, scope string) context.Context {
	t.Helper()
	token, err := f.minter.Mint(principal.Principal{ID: "p-" + scope, Kind: principal.KindSession, Scope: scope}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.minter.Resolve(token, newTestClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

// appendTurn calls chat.append_turn and returns the result's thread id.
func (f *scopeFixture) appendTurn(ctx context.Context, threadID string) (string, *rpc.ErrorObject) {
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": "hello"}}}
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

// state is a thread's turn count and scope row ("" when unattributed).
func (f *scopeFixture) state(t *testing.T, threadID string) (int, string) {
	t.Helper()
	turns, err := f.store.ListTurns(context.Background(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureScopeSchema(context.Background(), f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	var scope string
	err = f.db.QueryRow(`SELECT scope_ref FROM `+tableThreadScope+` WHERE thread_id = ?`, threadID).Scan(&scope)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return len(turns), scope
}

func seedThread(t *testing.T, s Store, threadID string) {
	t.Helper()
	turn := Turn{ID: NewTurnID(threadID, 0, RoleUser), ThreadID: threadID, Role: RoleUser, CreatedAt: 1}
	if err := s.AppendTurn(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
}

func refusedDenied(eo *rpc.ErrorObject) bool {
	return eo != nil && eo.Code == cascade.KindPermissionDenied.JSONRPCCode()
}

func TestThreadScopeMigrationRoundTrip(t *testing.T) {
	legs := map[string]func(t *testing.T) *sql.DB{
		"sqlite": openTestDB,
		"memstore": func(t *testing.T) *sql.DB {
			db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			return db
		},
	}
	for name, open := range legs {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := open(t)
			if err := ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
				t.Fatal(err)
			}
			s := NewStore(db)
			seedThread(t, s, "pre-migration")
			for i := 0; i < 2; i++ { // the second apply is the ledger's no-op
				if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}, scopeMigrationSet()); err != nil {
					t.Fatalf("apply %d: %v", i, err)
				}
			}
			var version int
			if err := db.QueryRow(`SELECT MAX(schema_version) FROM applied_migrations WHERE set_id = ?`, "conversation-thread-scope").Scan(&version); err != nil || version != 1 {
				t.Fatalf("ledger version %d: %v", version, err)
			}
			for _, scope := range []string{ownerScope, "project:a"} {
				if ids, err := scopedThreadIDs(ctx, db, scope); err != nil || len(ids) != 0 {
					t.Fatalf("pre-migration thread attributed to %s: %v %v", scope, ids, err)
				}
			}
			if err := runTx(ctx, db, func(tx *sql.Tx) error { return attributeThread(tx, "new", "project:a") }); err != nil {
				t.Fatal(err)
			}
			if ids, err := scopedThreadIDs(ctx, db, "project:a"); err != nil || len(ids) != 1 || !ids["new"] {
				t.Fatalf("attributed thread read back as %v: %v", ids, err)
			}
		})
	}
	t.Run("postgres", func(t *testing.T) {
		if postgresScopeLeg == nil {
			t.Skip("postgres leg runs under -tags postgres with CASCADE_TEST_POSTGRES_DSN")
		}
		postgresScopeLeg(t)
	})
}

func TestAttributeThreadCreateOnly(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t, openTestDB(t))
	if err := ensureScopeSchema(ctx, f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	write := func(scope string) error {
		return runTx(ctx, f.db, func(tx *sql.Tx) error { return attributeThread(tx, "t1", scope) })
	}
	if err := write("project:a"); err != nil {
		t.Fatal(err)
	}
	if err := write("project:a"); err != nil {
		t.Fatalf("same-scope repeat: %v", err)
	}
	err := write("project:b")
	if kind, _ := cascade.KindOf(err); kind != cascade.KindConflict || err.Error() != errScopeConflict.Error() {
		t.Fatalf("rewrite to another scope: %v", err)
	}
	if _, scope := f.state(t, "t1"); scope != "project:a" {
		t.Fatalf("scope rewritten to %q", scope)
	}
	if err := write(""); err != ErrInvalidRecord {
		t.Fatalf("empty scope: %v", err)
	}
}

func TestPrincipalCannotClaimLegacyThread(t *testing.T) {
	f := newScopeFixture(t, openTestDB(t))
	seedThread(t, f.store, "legacy")
	turns, scope := f.state(t, "legacy")
	if _, eo := f.appendTurn(f.principalCtx(t, "project:a"), "legacy"); !refusedDenied(eo) {
		t.Fatalf("principal append to a legacy thread: %+v", eo)
	}
	if n, s := f.state(t, "legacy"); n != turns || s != scope || s != "" {
		t.Fatalf("legacy thread changed: turns %d->%d scope %q->%q", turns, n, scope, s)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, eo := f.appendTurn(owner, "legacy"); eo != nil {
		t.Fatal(eo)
	}
	if n, s := f.state(t, "legacy"); n != turns+1 || s != "" {
		t.Fatalf("owner append attributed the legacy thread: turns %d scope %q", n, s)
	}
}

func TestPrincipalAppendForeignThreadRefused(t *testing.T) {
	f := newScopeFixture(t, openTestDB(t))
	a, b := f.principalCtx(t, "project:a"), f.principalCtx(t, "project:b")
	bThread, eo := f.appendTurn(b, "")
	if eo != nil {
		t.Fatal(eo)
	}
	if _, eo := f.appendTurn(a, bThread); !refusedDenied(eo) {
		t.Fatalf("project:a append to project:b thread: %+v", eo)
	}
	if n, s := f.state(t, bThread); n != 1 || s != "project:b" {
		t.Fatalf("foreign thread changed: turns %d scope %q", n, s)
	}
	if _, eo := f.appendTurn(a, "named-by-a"); !refusedDenied(eo) {
		t.Fatalf("principal created a thread by naming it: %+v", eo)
	}
	if n, s := f.state(t, "named-by-a"); n != 0 || s != "" {
		t.Fatalf("refused append wrote: turns %d scope %q", n, s)
	}
	if _, eo := f.appendTurn(b, bThread); eo != nil {
		t.Fatalf("own-scope append: %+v", eo)
	}
}

func TestNonProjectPrincipalRefusedBeforeAnyWrite(t *testing.T) {
	f := newScopeFixture(t, openTestDB(t))
	ctx := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "p-personal", Kind: principal.KindSession, Scope: "personal"}})
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": "hello"}}, "privacy_mode": "local-only"}
	raw, _ := json.Marshal(params)
	_, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: MethodAppendTurn, Params: raw})
	if !refusedDenied(eo) {
		t.Fatalf("want permission denied, got %+v", eo)
	}
	if err := ensureScopeSchema(context.Background(), f.db, newTestClock()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"conversation_thread_privacy", "conversation_thread", "conversation_thread_scope"} {
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s has %d rows after a refused append", table, n)
		}
	}
}
