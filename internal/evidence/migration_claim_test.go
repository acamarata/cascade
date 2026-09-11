// Purpose: shared test fixtures (openTestDB/newStore, real modernc-sqlite
// over t.TempDir per Art.2/Art.7.1) plus MigrationSet's own tests: table
// and index presence via sqlite_master, idempotent re-apply.
//
// SPORT: evidence/claim-record (ADD).
package evidence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// fixedTime is the frozen instant every test in this package's clock uses
// (Art.7.3: no bare time.Now anywhere in this package's tests).
var fixedTime = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

// openTestDB opens a REAL modernc-sqlite database file under t.TempDir()
// (Art.2: a real counterpart, never an in-memory self-authored double).
// See testdata/expand/README.md for the provenance this satisfies.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
	}
	if err := ApplySchema(context.Background(), db, migrate.SQLiteEmitter{}, testkit.NewFrozenClock(fixedTime), "", ""); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	return db
}

// newStore returns a Store over a fresh openTestDB, clocked by a
// FrozenClock (Art.7.3: no bare time.Now in test assertions either).
func newStore(t *testing.T) (*Store, *testkit.FrozenClock) {
	t.Helper()
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(fixedTime)
	return NewStore(db, clock), clock
}

func TestMigrationCreatesTablesAndIndexesIdempotently(t *testing.T) {
	db := openTestDB(t)
	for _, table := range []string{tableClaim, tableClaimEvidence} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s not created: %v", table, err)
		}
	}
	for _, idx := range []string{"idx_jobs_claim_run_id", "idx_jobs_claim_evidence_claim_id"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name); err != nil {
			t.Fatalf("index %s not created: %v", idx, err)
		}
	}
	// Re-apply must be a no-op, never an error.
	if err := ApplySchema(context.Background(), db, migrate.SQLiteEmitter{}, testkit.NewFrozenClock(fixedTime), "", ""); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

func TestApplySchemaRefusesNilArgs(t *testing.T) {
	if err := ApplySchema(context.Background(), nil, migrate.SQLiteEmitter{}, testkit.NewFrozenClock(fixedTime), "", ""); err == nil {
		t.Fatal("ApplySchema with nil db = nil error, want invalid-input")
	}
	db := openTestDB(t)
	if err := ApplySchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplySchema with nil clock = nil error, want invalid-input")
	}
}
