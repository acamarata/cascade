package topology

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests (Art.7.3 -- no bare time.Now).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock { return fakeClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)} }

// openRealSQLiteFile opens a REAL, file-backed modernc SQLite database
// under t.TempDir (Art.2.1/2.2's "real counterpart" rule -- never an
// in-memory-only stand-in for this ticket's migration proof).
func openRealSQLiteFile(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open real sqlite file: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrationRealSQLite applies MigrationSet against a REAL modernc
// SQLite database on a t.TempDir file, then re-applies it to prove
// idempotence, per Art.2.1.
func TestMigrationRealSQLite(t *testing.T) {
	db := openRealSQLiteFile(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("first ApplyMigrationSchema on real sqlite file: %v", err)
	}
	for _, table := range []string{tableAccount, tableCredential, tableQuotaDomain, tableRuntimeProfile, tableLane} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
	// R-14.5/R-16.51/R-16.75 domain list stays unchanged: this ticket never
	// registers a new DomainID, it only adds tables to the existing
	// `config` domain (R-21.22) -- there is nothing to assert against
	// storage.AllDomains because this package never calls it (see this
	// ticket's journal for the full contract-vs-tree note).

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema (re-apply) should be a no-op, got: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(nil db, ...) should have failed")
	}
	db := openRealSQLiteFile(t)
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(..., nil clock, ...) should have failed")
	}
}

// TestMigrationSetPostgresEmitSucceeds proves MigrationSet emits valid DDL
// under the Postgres dialect too (no live Postgres in this sandbox).
func TestMigrationSetPostgresEmitSucceeds(t *testing.T) {
	if _, err := (migrate.PostgresEmitter{}).Emit(MigrationSet()); err != nil {
		t.Fatalf("PostgresEmitter.Emit: %v", err)
	}
}

func TestMigrationSetIdentity(t *testing.T) {
	set := MigrationSet()
	if set.SetID != "fleet-topology" {
		t.Errorf("SetID = %q, want \"fleet-topology\"", set.SetID)
	}
	if set.SchemaVersion != topologySchemaVersion || set.ReaderCeiling != topologySchemaVersion {
		t.Errorf("SchemaVersion/ReaderCeiling should both equal topologySchemaVersion")
	}
	if len(set.Steps) != 5 {
		t.Fatalf("expected 5 migration steps (one per entity table), got %d", len(set.Steps))
	}
}
