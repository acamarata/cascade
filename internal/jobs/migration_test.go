// Purpose: shared test fixtures (openTestDB/newTestClock/newTestStore/
//
//	baseJob, real modernc-sqlite over t.TempDir per Art.2/Art.7.1) plus
//	MigrationSet's own tests: table presence via sqlite_master, idempotent
//	re-apply, and nil-arg refusal.
//
// SPORT: jobs/domain-schema/ADD (P1-E29-W6-S59-T1).
package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests -- no bare time.Now (Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() migrate.Clock {
	return fakeClock{t: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a REAL modernc-sqlite database file under t.TempDir()
// (Art.2: a real counterpart, never an in-memory self-authored double).
// See testdata/README.md for the provenance this satisfies.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestStore opens a fresh real db, applies the jobs migration, and
// returns a Store over it.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	return NewStore(db)
}

// baseJob returns a minimally-valid Job for tests to mutate.
func baseJob(id string) Job {
	return Job{
		ID:               id,
		State:            JobStatePending,
		CreatedAt:        1,
		UpdatedAt:        1,
		Capabilities:     []string{"code"},
		MutableScope:     "repo:/tmp/x",
		RiskClass:        "normal",
		MinTaskClass:     "code",
		NodeRequirements: "{}",
		TimeoutSeconds:   60,
		CostCeiling:      1.0,
		Priority:         1,
		ConsequenceClass: ConsequenceNormal,
		DataClass:        DataClassInternal,
	}
}

func TestJobsMigrationCreatesSevenTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	for _, table := range []string{
		tableJob, tableTaskDependency, tableExecution, tableExecutionResult,
		tableArtifact, tableResourceLease, tableWorktree,
	} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='jobs_ci_attestation'`).Scan(&name)
	if err == nil {
		t.Error("jobs_ci_attestation table exists; AF/S-65.T4 owns it, not this ticket")
	}
}

func TestJobsMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("first ApplyJobsSchema: %v", err)
	}
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("second ApplyJobsSchema: %v", err)
	}
}

func TestJobsMigrationRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Error("ApplyJobsSchema(nil db) = nil, want error")
	}
	db := openTestDB(t)
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("ApplyJobsSchema(nil clock) = nil, want error")
	}
}

// TestJobsMigrationW9W6Columns confirms every W9 (R-21.84/94/99) and W6
// (R-21.139/140/172/177) column landed via a real PRAGMA table_info
// assertion against the real db, not a parsed copy of migration.go's own
// TableDef.
func TestJobsMigrationW9W6Columns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	want := map[string][]string{
		tableJob:           {"consecutive_failed_attempts", "consequence_class", "data_class"},
		tableArtifact:      {"consequence_class", "data_class"},
		tableResourceLease: {"epoch", "state"},
		tableExecution:     {"pgid", "heartbeat_at"},
	}
	for table, cols := range want {
		got := tableColumns(t, db, table)
		for _, col := range cols {
			if !got[col] {
				t.Errorf("table %s missing column %s (got %v)", table, col, got)
			}
		}
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		cols[name] = true
	}
	return cols
}
