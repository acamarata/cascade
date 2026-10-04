// Purpose: RetentionSweep tests -- zero rows (no-op), age boundary
//
//	(older deleted, newer retained), MaxAge config override, closed-db
//	error, and nil-collaborator refusal.
//
// SPORT: learn/retention/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// fixedPaths is a minimal runtime.PathProvider stub naming a real
// t.TempDir() config.toml -- Run genuinely reads it via runtime.Load
// (Art.7.1: never $HOME).
type fixedPaths struct{ configPath string }

func (p fixedPaths) Root() string                       { return filepath.Dir(p.configPath) }
func (p fixedPaths) ConfigPath() string                 { return p.configPath }
func (p fixedPaths) DataDir() string                    { return filepath.Dir(p.configPath) }
func (p fixedPaths) LogDir() string                     { return filepath.Dir(p.configPath) }
func (p fixedPaths) SocketPath() string                 { return filepath.Join(filepath.Dir(p.configPath), "d.sock") }
func (p fixedPaths) StorageRoot(runtime.Profile) string { return filepath.Dir(p.configPath) }

func newFixedPaths(t *testing.T, configBody string) fixedPaths {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if configBody != "" {
		if err := os.WriteFile(path, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
	}
	return fixedPaths{configPath: path}
}

func noEnv(string) string { return "" }
func noEnviron() []string { return nil }

// insertOutcomeAt inserts a minimal jobs_telemetry_outcomes row with an
// explicit created_at, for retention boundary tests.
func insertOutcomeAt(t *testing.T, db *sql.DB, jobID string, createdAt int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO `+tableTelemetryOutcomes+`
		(job_id, task_class, repo_id, language, component, risk_class, lane_tier, node_id, scope_ref,
		 context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count,
		 ci_failure_count, rework_cycles, final_outcome, regression_detected, cost_tokens, quota_units, created_at)
		VALUES (?,'code','r','go','c','normal','t1','n1','s1',0,'r',0,0,0,0,0,'accepted',0,0,0,?)`,
		jobID, createdAt)
	if err != nil {
		t.Fatalf("insertOutcomeAt(%s): %v", jobID, err)
	}
}

func assertOutcomeExists(t *testing.T, db *sql.DB, jobID string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, jobID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", jobID, err)
	}
	if got := count > 0; got != want {
		t.Errorf("job_id %s exists = %v, want %v", jobID, got, want)
	}
}

// TestRetentionSweep_ZeroRows: an empty table is a no-op.
func TestRetentionSweep_ZeroRows(t *testing.T) {
	db := newTestOutcomeDB(t)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
	sweep := RetentionSweep{DB: db, Clock: clock, Paths: newFixedPaths(t, ""), Getenv: noEnv, Environ: noEnviron}
	n, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d rows deleted, want 0", n)
	}
}

// TestRetentionSweepBoundary: rows older than MaxAge are deleted; rows
// within MaxAge survive.
func TestRetentionSweepBoundary(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -100).Unix() // older than the 90-day default
	fresh := now.AddDate(0, 0, -10).Unix()
	insertOutcomeAt(t, db, "job-old", old)
	insertOutcomeAt(t, db, "job-fresh", fresh)

	clock := runtime.NewFixedClock(now)
	sweep := RetentionSweep{DB: db, Clock: clock, Paths: newFixedPaths(t, ""), Getenv: noEnv, Environ: noEnviron}
	n, err := sweep.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d rows deleted, want 1", n)
	}
	assertOutcomeExists(t, db, "job-old", false)
	assertOutcomeExists(t, db, "job-fresh", true)
}

// TestRetentionSweepMaxAgeOverride: a config.toml override changes the
// cutoff a fresh Run applies.
func TestRetentionSweepMaxAgeOverride(t *testing.T) {
	db := newTestOutcomeDB(t)
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	insertOutcomeAt(t, db, "job-15d", now.AddDate(0, 0, -15).Unix())

	clock := runtime.NewFixedClock(now)
	paths := newFixedPaths(t, "[learn.retention]\nmax_age_days = 10\n")
	sweep := RetentionSweep{DB: db, Clock: clock, Paths: paths, Getenv: noEnv, Environ: noEnviron}
	n, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d rows deleted with max_age_days=10 against a 15-day-old row, want 1", n)
	}
	assertOutcomeExists(t, db, "job-15d", false)
}

// TestRetentionSweepClosedDB: a closed db surfaces a typed error, not a panic.
func TestRetentionSweepClosedDB(t *testing.T) {
	db := newTestOutcomeDB(t)
	_ = db.Close()
	clock := runtime.NewFixedClock(time.Now())
	sweep := RetentionSweep{DB: db, Clock: clock, Paths: newFixedPaths(t, ""), Getenv: noEnv, Environ: noEnviron}
	if _, err := sweep.Run(context.Background()); err == nil {
		t.Error("Run on a closed db = nil error, want a failure")
	}
}

// TestRetentionSweepRequiresCollaborators: a zero-value RetentionSweep refuses.
func TestRetentionSweepRequiresCollaborators(t *testing.T) {
	sweep := RetentionSweep{}
	if _, err := sweep.Run(context.Background()); err == nil {
		t.Error("Run with zero-value RetentionSweep = nil error, want refusal")
	}
}

// TestRegisterRetentionTableJoinsSweep: a table registered through
// RegisterRetentionTable is swept by the same RetentionSweep run, with
// the same age boundary as the built-in tables; a duplicate registration
// and a non-identifier table name both refuse. The table name is unique
// to this test (never reused by another test in this package) since
// RegisterRetentionTable has no matching Unregister and the registry is
// package-global for the whole test binary.
func TestRegisterRetentionTableJoinsSweep(t *testing.T) {
	const widgetTable = "test_retention_widget_p1_cap_02"
	db := newTestOutcomeDB(t)
	if _, err := db.Exec(`CREATE TABLE ` + widgetTable + ` (id INTEGER PRIMARY KEY, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create %s: %v", widgetTable, err)
	}
	if err := RegisterRetentionTable(widgetTable, "created_at"); err != nil {
		t.Fatalf("RegisterRetentionTable: %v", err)
	}

	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -100).Unix()
	fresh := now.AddDate(0, 0, -10).Unix()
	if _, err := db.Exec(`INSERT INTO `+widgetTable+` (id, created_at) VALUES (1, ?), (2, ?)`, old, fresh); err != nil {
		t.Fatalf("seed %s: %v", widgetTable, err)
	}

	clock := runtime.NewFixedClock(now)
	sweep := RetentionSweep{DB: db, Clock: clock, Paths: newFixedPaths(t, ""), Getenv: noEnv, Environ: noEnviron}
	n, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d rows deleted, want 1 (the widget row swept alongside the built-in tables)", n)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + widgetTable).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", widgetTable, err)
	}
	if count != 1 {
		t.Errorf("%s rows after sweep = %d, want 1", widgetTable, count)
	}

	if err := RegisterRetentionTable(widgetTable, "created_at"); err == nil {
		t.Error("RegisterRetentionTable duplicate = nil error, want refusal")
	}
	if err := RegisterRetentionTable("bad; name", "created_at"); err == nil {
		t.Error("RegisterRetentionTable with a non-identifier table name = nil error, want refusal")
	}
	if err := RegisterRetentionTable(widgetTable+"_2", "bad column"); err == nil {
		t.Error("RegisterRetentionTable with a non-identifier time column = nil error, want refusal")
	}
}
