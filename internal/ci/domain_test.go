// Purpose: domain.go tests: MigrationSet applies cleanly under the
//
//	real B/S-02.T3 harness against a live sqlite database (registry.go's
//	and jobs/migration.go's own precedent -- no live Postgres is
//	reachable from this sandbox, so the postgres dialect is asserted by
//	successful DDL emission, matching every sibling package's own test
//	depth), plus Upsert's idempotency guarantee (overlapping polling
//	passes over the same run produce no duplicate ci_run/ci_job/ci_step
//	rows).
//
// SPORT: internal.ci.MigrationSet/TESTED, internal.ci.Upsert/TESTED
//
//	(P1-E25-W5-S51-T2).
package ci

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestApplyMigrationSchema_SQLite applies MigrationSet against a live
// in-memory SQLite database and confirms all three tables exist.
func TestApplyMigrationSchema_SQLite(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	for _, table := range []string{tableRun, tableJob, tableStep} {
		var name string
		row := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table)
		if err := row.Scan(&name); err != nil {
			t.Errorf("table %s: not found after ApplyMigrationSchema: %v", table, err)
		}
	}
}

// TestApplyMigrationSchema_Idempotent asserts calling ApplyMigrationSchema
// twice on the same database is a no-op the second time (§5.9).
func TestApplyMigrationSchema_Idempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()
	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("second apply: %v", err)
	}
}

// TestMigrationSetReferenceShape asserts every table/column named in
// migrations/001_ci_results.sql (the reference-only rendering) is present
// in the real emitted SQLite DDL, and that the Postgres emitter also
// succeeds (no live Postgres in this sandbox -- successful dialect-correct
// emission is the achievable, honest assertion, matching every sibling
// package's identical test depth).
func TestMigrationSetReferenceShape(t *testing.T) {
	sqliteDDL, err := migrate.SQLiteEmitter{}.Emit(MigrationSet())
	if err != nil {
		t.Fatalf("SQLiteEmitter.Emit: %v", err)
	}
	joined := strings.Join(sqliteDDL, "\n")
	for _, want := range []string{tableRun, tableJob, tableStep, "run_id", "repo_id", "job_id", "number"} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted SQLite DDL missing %q", want)
		}
	}
	if _, err := (migrate.PostgresEmitter{}).Emit(MigrationSet()); err != nil {
		t.Fatalf("PostgresEmitter.Emit: %v", err)
	}
}

func sampleRun(runID int64) Run {
	return Run{
		RunID: runID, RepoID: 7, Name: "ci", HeadBranch: "main", HeadSHA: "abc123",
		Status: RunStatusCompleted, Conclusion: ConclusionSuccess,
		CreatedAt: newTestClock().Now(), UpdatedAt: newTestClock().Now(),
	}
}

// TestUpsert_IdempotentOnOverlappingPolls asserts two overlapping polling
// passes over the SAME run/job/step produce exactly one row each, keyed
// on (run_id, repo_id) for ci_run and on the natural key for ci_job/
// ci_step -- the ticket's own idempotent-upsert acceptance criterion.
func TestUpsert_IdempotentOnOverlappingPolls(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}

	run := sampleRun(100)
	job := Job{JobID: 200, RunID: 100, Name: "build", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	step := Step{JobID: 200, Number: 1, Name: "checkout", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}

	if err := Upsert(ctx, db, run, []Job{job}, []Step{step}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// A second, overlapping polling pass observes the run as still
	// completed but with an updated Conclusion (e.g. a retry flipped it).
	run.Conclusion = ConclusionFailure
	if err := Upsert(ctx, db, run, []Job{job}, []Step{step}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	assertRowCount(t, db, tableRun, 1)
	assertRowCount(t, db, tableJob, 1)
	assertRowCount(t, db, tableStep, 1)

	var conclusion string
	row := db.QueryRowContext(ctx, `SELECT conclusion FROM `+tableRun+` WHERE run_id=? AND repo_id=?`, run.RunID, run.RepoID)
	if err := row.Scan(&conclusion); err != nil {
		t.Fatalf("reading updated conclusion: %v", err)
	}
	if conclusion != string(ConclusionFailure) {
		t.Errorf("conclusion = %q after the second pass, want %q (upsert must update, not duplicate)", conclusion, ConclusionFailure)
	}
}

// TestUpsert_DistinctRepoDoesNotCollide asserts the SAME run_id under a
// DIFFERENT repo_id is a distinct row -- the composite key is
// (run_id, repo_id), never run_id alone.
func TestUpsert_DistinctRepoDoesNotCollide(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	r1 := sampleRun(1)
	r1.RepoID = 1
	r2 := sampleRun(1)
	r2.RepoID = 2
	if err := Upsert(ctx, db, r1, nil, nil); err != nil {
		t.Fatalf("upsert repo 1: %v", err)
	}
	if err := Upsert(ctx, db, r2, nil, nil); err != nil {
		t.Fatalf("upsert repo 2: %v", err)
	}
	assertRowCount(t, db, tableRun, 2)
}

func assertRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatalf("counting %s rows: %v", table, err)
	}
	if got != want {
		t.Errorf("%s row count = %d, want %d", table, got, want)
	}
}

// TestApplyMigrationSchema_NilArgsRefused covers the two fail-closed
// guard clauses.
func TestApplyMigrationSchema_NilArgsRefused(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Error("expected an error for a nil db")
	}
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("expected an error for a nil Clock")
	}
}

// TestUpsert_ErrorPropagation covers Upsert's three error-return branches
// (run, job, step) by dropping each table before the write reaches it.
func TestUpsert_ErrorPropagation(t *testing.T) {
	ctx := context.Background()

	dbRun := openTestDB(t)
	if err := Upsert(ctx, dbRun, sampleRun(1), nil, nil); err == nil {
		t.Error("expected an error writing a run with no schema applied")
	}

	dbJob := openTestDB(t)
	if err := ApplyMigrationSchema(ctx, dbJob, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	if _, err := dbJob.ExecContext(ctx, "DROP TABLE "+tableJob); err != nil {
		t.Fatalf("dropping %s: %v", tableJob, err)
	}
	job := Job{JobID: 1, RunID: 1, Name: "x", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	if err := Upsert(ctx, dbJob, sampleRun(1), []Job{job}, nil); err == nil {
		t.Error("expected an error writing a job whose table was dropped")
	}

	dbStep := openTestDB(t)
	if err := ApplyMigrationSchema(ctx, dbStep, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	if _, err := dbStep.ExecContext(ctx, "DROP TABLE "+tableStep); err != nil {
		t.Fatalf("dropping %s: %v", tableStep, err)
	}
	step := Step{JobID: 1, Number: 1, Name: "x", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	if err := Upsert(ctx, dbStep, sampleRun(1), nil, []Step{step}); err == nil {
		t.Error("expected an error writing a step whose table was dropped")
	}
}
