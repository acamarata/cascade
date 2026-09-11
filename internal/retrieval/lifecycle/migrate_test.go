package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for Migrate — the ledger,
// idempotency, and older-binary-refusal behaviors the B/S-02.T3 builder
// provides, exercised for real against a real sqlite database.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dbPath
}

func migrateDeps(db *sql.DB, dbPath string, clock runtime.Clock) lifecycle.MigrateDeps {
	return lifecycle.MigrateDeps{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock,
		DBPath: dbPath, BackupDir: filepath.Join(filepath.Dir(dbPath), "backups"),
	}
}

// TestRecallIndexMigrateRunsCleanTwice proves Migrate runs the real
// migrate.Apply pipeline (ledger, snapshot, downgrade check) twice
// without error over RetrievalMigrationSet, and — since that set carries
// no steps today (this file's CONTRACT NOTE) — both runs honestly report
// nothing applied rather than fabricating a converged signal the ledger
// never records for an empty step set.
func TestRecallIndexMigrateRunsCleanTwice(t *testing.T) {
	db, dbPath := openTestDB(t)
	clock := runtime.NewFixedClock(fixedNow)
	deps := migrateDeps(db, dbPath, clock)

	first, err := lifecycle.Migrate(context.Background(), deps)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if first.Applied || first.ToVersion != lifecycle.SchemaVersion {
		t.Fatalf("want ToVersion %d and Applied=false (no steps to converge), got %+v", lifecycle.SchemaVersion, first)
	}

	second, err := lifecycle.Migrate(context.Background(), deps)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second.Applied {
		t.Fatalf("want the second, unchanged migrate to report nothing to do, got %+v", second)
	}
}

// TestRecallIndexMigrateOlderBinaryRefusal proves a binary whose
// ReaderCeiling is below the on-disk schema_version is refused.
// The on-disk version only ever advances through a real ledger row, which
// migrate.Apply writes only for a set that carries at least one step —
// RetrievalMigrationSet does not (this file's CONTRACT NOTE) — so this
// test advances the ledger with a helper set carrying one harmless
// CreateTable step, then proves lifecycle.Migrate refuses to reopen it.
func TestRecallIndexMigrateOlderBinaryRefusal(t *testing.T) {
	db, dbPath := openTestDB(t)
	clock := runtime.NewFixedClock(fixedNow)
	ctx := context.Background()

	futureVersion := lifecycle.SchemaVersion + 1
	future := migrate.MigrationSet{
		SetID:         "lifecycle", // same SetID lifecycle.Migrate uses (R-16.77) — this simulates a NEWER version of the SAME set already on disk.
		SchemaVersion: futureVersion, ReaderCeiling: futureVersion,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable, Description: "test: advance the ledger past this binary",
			Table: &migrate.TableDef{Name: "lifecycle_test_future", Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			}},
		}},
	}
	if err := migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock, DBPath: dbPath,
		BackupDir: filepath.Join(filepath.Dir(dbPath), "backups"),
	}, future); err != nil {
		t.Fatalf("advance to future version: %v", err)
	}

	_, err := lifecycle.Migrate(ctx, migrateDeps(db, dbPath, clock))
	if err == nil {
		t.Fatal("want an older-binary refusal, got nil error")
	}
}

// TestRecallIndexMigrateRequiresDeps proves Migrate validates its
// required fields before any I/O.
func TestRecallIndexMigrateRequiresDeps(t *testing.T) {
	db, dbPath := openTestDB(t)
	clock := runtime.NewFixedClock(fixedNow)
	base := migrateDeps(db, dbPath, clock)

	noDB := base
	noDB.DB = nil
	if _, err := lifecycle.Migrate(context.Background(), noDB); err == nil {
		t.Fatal("want an error with no DB")
	}

	noDialect := base
	noDialect.Dialect = nil
	if _, err := lifecycle.Migrate(context.Background(), noDialect); err == nil {
		t.Fatal("want an error with no dialect")
	}

	noClock := base
	noClock.Clock = nil
	if _, err := lifecycle.Migrate(context.Background(), noClock); err == nil {
		t.Fatal("want an error with no clock")
	}
}
