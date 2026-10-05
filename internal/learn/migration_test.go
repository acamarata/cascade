// Purpose: shared test fixtures (openTestDB/newTestClock, real
//
//	modernc-sqlite over t.TempDir per Art.2/Art.7.1; openMigratedDB, a byte
//	copy of a database the real migrations built once per package run) plus
//	MigrationSet's own tests: table/index presence, idempotent re-apply, and
//	proof that each template equals a fresh migration.
//
// SPORT: learn/migration/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests -- no bare time.Now (Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a REAL modernc-sqlite database file under t.TempDir()
// (Art.2/Art.7), unmigrated. Tests that assert migration behaviour use it and
// run the real migration themselves.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openDBAt(t, filepath.Join(t.TempDir(), "learn-test.db"))
}

func openDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// templateKind names a set of schemas migrated into one template file.
type templateKind int

const (
	tmplOutcome     templateKind = iota // learn + conductor usage (newTestOutcomeDB)
	tmplJobs                            // jobs + learn (newTestJobsStore)
	tmplOutcomeJobs                     // learn + usage + jobs (the reconciler's outcome db)
	tmplKinds
)

// templateSchemas lists, in apply order, the real migrations behind each kind.
var templateSchemas = map[templateKind][]string{
	tmplOutcome:     {"learn", "usage"},
	tmplJobs:        {"jobs", "learn"},
	tmplOutcomeJobs: {"learn", "usage", "jobs"},
}

// templateDir holds the template files for one package run; TestMain removes it.
var (
	templateDir  string
	templateOnce [tmplKinds]sync.Once
	templateErr  [tmplKinds]error
)

// applySchema runs one REAL migration against db.
func applySchema(db *sql.DB, name string) error {
	ctx, d, c := context.Background(), migrate.SQLiteEmitter{}, newTestClock()
	switch name {
	case "learn":
		return ApplyLearnSchema(ctx, db, d, c)
	case "usage":
		return conductor.ApplyUsageMigrationSchema(ctx, db, d, c, "", "")
	default:
		return jobs.ApplyJobsSchema(ctx, db, d, c, "", "")
	}
}

// templatePath migrates a database with the real migrations ONCE per package
// run (per kind) and returns its path; callers copy the file, never write it.
func templatePath(kind templateKind) (string, error) {
	path := filepath.Join(templateDir, "template-"+strconv.Itoa(int(kind))+".db")
	templateOnce[kind].Do(func() { templateErr[kind] = buildTemplate(kind, path) })
	return path, templateErr[kind]
}

func buildTemplate(kind templateKind, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	for _, name := range templateSchemas[kind] {
		if err = applySchema(db, name); err != nil {
			_ = db.Close()
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return db.Close() // closed: the file is complete, no journal left behind
}

// openMigratedDB returns a real file db under t.TempDir() that is a byte copy
// of the once-migrated template for kind: migrate once, copy the file.
func openMigratedDB(t *testing.T, kind templateKind) *sql.DB {
	t.Helper()
	src, err := templatePath(kind)
	if err != nil {
		t.Fatalf("build migrated template %d: %v", kind, err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read template %d: %v", kind, err)
	}
	path := filepath.Join(t.TempDir(), "learn-test.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy template %d: %v", kind, err)
	}
	return openDBAt(t, path)
}

// TestMain owns the template directory for the whole package run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "learn-templates-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "learn tests: template dir:", err)
		os.Exit(1)
	}
	templateDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// schemaSQL is the sorted sqlite_master DDL of db (the migrated shape).
func schemaSQL(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type || ':' || name || ':' || COALESCE(sql,'') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan schema: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema rows: %v", err)
	}
	return out
}

// TestTemplateMatchesFreshMigration proves every copied template is what the
// real migrations build: its schema equals a fresh migration of the same set.
func TestTemplateMatchesFreshMigration(t *testing.T) {
	for kind := templateKind(0); kind < tmplKinds; kind++ {
		fresh := openTestDB(t)
		for _, name := range templateSchemas[kind] {
			if err := applySchema(fresh, name); err != nil {
				t.Fatalf("kind %d %s: %v", kind, name, err)
			}
		}
		want := schemaSQL(t, fresh)
		got := schemaSQL(t, openMigratedDB(t, kind))
		if len(got) < 5 || !slices.Equal(got, want) {
			t.Errorf("kind %d: template schema (%d objects) differs from a fresh migration (%d objects)", kind, len(got), len(want))
		}
	}
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

// TestMigrationIdempotent: MigrationSet (SchemaVersion 2) applies twice with
// nil, and on top of a SchemaVersion 1 database (the P1-CAP-02 shape) twice
// with nil, leaving the ledger at version 2, every v2 index present and no
// free-text column on jobs_scheduler_decisions.
func TestMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	fresh, upgraded := openTestDB(t), openTestDB(t)
	v1 := migrate.ApplyConfig{DB: upgraded, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, v1, migrationSetAt(1)); err != nil {
		t.Fatalf("apply SchemaVersion 1: %v", err)
	}
	if got := MigrationSet().SchemaVersion; got != 2 {
		t.Fatalf("MigrationSet().SchemaVersion = %d, want 2", got)
	}
	for name, db := range map[string]*sql.DB{"fresh": fresh, "over version 1": upgraded} {
		for pass := 1; pass <= 2; pass++ {
			if err := ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock()); err != nil {
				t.Fatalf("%s: ApplyLearnSchema pass %d: %v", name, pass, err)
			}
		}
		var top int
		if err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations WHERE set_id = ?`, learnSetID).Scan(&top); err != nil || top != 2 {
			t.Errorf("%s: ledger top version = %d (err %v), want 2", name, top, err)
		}
		for _, idx := range []string{"idx_jobs_capability_score_key", "idx_jobs_scheduler_decisions_job_id",
			"idx_jobs_scheduler_decisions_execution_id", "idx_jobs_scheduler_decisions_decided_at",
			"idx_jobs_telemetry_outcomes_repo_created", "idx_jobs_scheduler_decisions_tier_class"} {
			var found string
			if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&found); err != nil {
				t.Errorf("%s: index %s missing: %v", name, idx, err)
			}
		}
		for _, c := range tableColumns(t, db, tableSchedulerDecision) {
			if freeTextColumn.MatchString(c) {
				t.Errorf("%s: jobs_scheduler_decisions column %q matches the free-text pattern", name, c)
			}
		}
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
