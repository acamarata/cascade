// Purpose: shared test fixtures (openTestDB/newTestClock, real
//
//	modernc-sqlite over t.TempDir per Art.2/Art.7.1) plus MigrationSet's
//	own tests: table/index presence and idempotent re-apply.
//
// SPORT: learn/migration/ADD (P1-E31-W6-S64-T1).
package learn

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

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a REAL modernc-sqlite database file under t.TempDir()
// (Art.2/Art.7).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "learn-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrationCreatesTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
		t.Fatalf("ApplyLearnSchema: %v", err)
	}
	for _, table := range []string{tableTelemetryOutcomes, tableTelemetryFinding} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
	for _, idx := range []string{
		"idx_jobs_telemetry_outcomes_job_id", "idx_jobs_telemetry_outcomes_node_id",
		"idx_jobs_telemetry_outcomes_task_lane", "idx_jobs_telemetry_outcomes_scope_ref",
		"idx_jobs_telemetry_outcomes_created_at",
	} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name)
		if err != nil {
			t.Errorf("index %s not created: %v", idx, err)
		}
	}
}

func TestMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
		t.Fatalf("first ApplyLearnSchema: %v", err)
	}
	if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
		t.Fatalf("second ApplyLearnSchema: %v", err)
	}
}

func TestApplyLearnSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyLearnSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock()); err == nil {
		t.Error("ApplyLearnSchema(nil db) = nil, want error")
	}
	db := openTestDB(t)
	if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, nil); err == nil {
		t.Error("ApplyLearnSchema(nil clock) = nil, want error")
	}
}

// jobIDUnique proves the job_id index is genuinely UNIQUE, not merely
// present -- a second outcome row for the same job_id must be refused by
// SQLite itself.
func TestTelemetryOutcomesJobIDUnique(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
		t.Fatalf("ApplyLearnSchema: %v", err)
	}
	insert := `INSERT INTO ` + tableTelemetryOutcomes + `
		(job_id, task_class, repo_id, language, component, risk_class, lane_tier, node_id, scope_ref,
		 context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count,
		 ci_failure_count, rework_cycles, final_outcome, regression_detected, cost_tokens, quota_units, created_at)
		VALUES ('job-1','code','r','go','c','normal','t1','n1','s1',0,'r',0,0,0,0,0,'accepted',0,0,0,1)`
	if _, err := db.ExecContext(ctx, insert); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert); err == nil {
		t.Error("second insert with the same job_id succeeded, want a UNIQUE constraint error")
	}
}
