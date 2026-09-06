package scope

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests -- no bare time.Now (Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() migrate.Clock {
	return fakeClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
}

// testHelper is the minimal subset of *testing.T and *testing.F this
// package's test helpers need. *testing.F deliberately does not implement
// testing.TB, so FuzzContextScopeShowParams (rpc_test.go) can still share
// openTestDB/newTestStore with every other test by satisfying this
// smaller interface instead.
type testHelper interface {
	Helper()
	Fatalf(format string, args ...any)
	Name() string
	Cleanup(func())
}

// openTestDB opens a fresh in-memory modernc SQLite database for one test.
// Each test gets its own named in-memory database (a unique DSN) so
// parallel tests never share state.
func openTestDB(t testHelper) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApplyScopeSchemaCreatesFourTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyScopeSchema: %v", err)
	}
	for _, table := range []string{tableScope, tableScopeEdge, tableRepository, tableRepoPath} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
}

func TestApplyScopeSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("first ApplyScopeSchema: %v", err)
	}
	if err := ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyScopeSchema: %v", err)
	}
}

func TestApplyScopeSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyScopeSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Error("ApplyScopeSchema(nil db) = nil, want error")
	}
	db := openTestDB(t)
	if err := ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("ApplyScopeSchema(nil clock) = nil, want error")
	}
}
