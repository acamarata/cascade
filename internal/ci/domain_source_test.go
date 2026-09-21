// Purpose: domain_source.go tests -- UpsertRunSource/runSource/ListRuns
// against a live in-memory SQLite database, plus the backward-compatible
// default (runSource for a row T2's own Upsert wrote, with no
// ci_run_source entry, reads back "github-actions").
// SPORT: internal.ci.UpsertRunSource/TESTED, internal.ci.runSource/TESTED,
//
//	internal.ci.ListRuns/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// TestMigrationSetReferenceShape_SourceTable extends the T2-authored
// TestMigrationSetReferenceShape's assertion set with the new table this
// ticket adds, proving the real emitted DDL (not just this file's own
// prose) carries it.
func TestMigrationSetReferenceShape_SourceTable(t *testing.T) {
	ddl, err := migrate.SQLiteEmitter{}.Emit(MigrationSet())
	if err != nil {
		t.Fatalf("SQLiteEmitter.Emit: %v", err)
	}
	joined := ""
	for _, s := range ddl {
		joined += s + "\n"
	}
	for _, want := range []string{tableRunSource, "source"} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted SQLite DDL missing %q", want)
		}
	}
}

// TestRunSource_DefaultsToGitHubActions asserts runSource's
// backward-compatibility default: a run T2's own Upsert wrote, with no
// ci_run_source row at all, reads back "github-actions" -- never "local"
// and never an error.
func TestRunSource_DefaultsToGitHubActions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	run := sampleRun(500)
	if err := Upsert(ctx, db, run, nil, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := runSource(ctx, db, run.RunID, run.RepoID)
	if err != nil {
		t.Fatalf("runSource: %v", err)
	}
	if got != SourceGitHubActions {
		t.Errorf("runSource = %q, want %q (no ci_run_source row must default to github-actions)", got, SourceGitHubActions)
	}
}

// TestUpsertRunSource_IdempotentAndOverwrites proves UpsertRunSource is
// idempotent (a second identical call does not duplicate the row) and
// that a changed source overwrites the prior value.
func TestUpsertRunSource_IdempotentAndOverwrites(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	run := sampleRun(501)
	if err := Upsert(ctx, db, run, nil, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := UpsertRunSource(ctx, db, run.RunID, run.RepoID, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource: %v", err)
	}
	if err := UpsertRunSource(ctx, db, run.RunID, run.RepoID, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource (repeat): %v", err)
	}
	assertRowCount(t, db, tableRunSource, 1)

	got, err := runSource(ctx, db, run.RunID, run.RepoID)
	if err != nil {
		t.Fatalf("runSource: %v", err)
	}
	if got != SourceLocal {
		t.Errorf("runSource = %q, want %q", got, SourceLocal)
	}
}

// TestUpsertRunSource_RejectsUnrecognisedSource proves the fail-closed
// guard: an arbitrary string is refused rather than silently stored.
func TestUpsertRunSource_RejectsUnrecognisedSource(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	run := sampleRun(502)
	if err := Upsert(ctx, db, run, nil, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := UpsertRunSource(ctx, db, run.RunID, run.RepoID, "bogus"); err == nil {
		t.Fatal("expected an error for an unrecognised source")
	}
}

// TestListRuns_CombinesBothSources proves ListRuns surfaces both a
// source=github-actions and a source=local row together, newest first.
func TestListRuns_CombinesBothSources(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	ghRun := sampleRun(600)
	if err := Upsert(ctx, db, ghRun, nil, nil); err != nil {
		t.Fatalf("Upsert gh run: %v", err)
	}
	localRun := sampleRun(601)
	localRun.UpdatedAt = localRun.UpdatedAt.Add(1)
	if err := Upsert(ctx, db, localRun, nil, nil); err != nil {
		t.Fatalf("Upsert local run: %v", err)
	}
	if err := UpsertRunSource(ctx, db, localRun.RunID, localRun.RepoID, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource: %v", err)
	}

	runs, err := ListRuns(ctx, db, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("ListRuns returned %d rows, want 2", len(runs))
	}
	sources := map[int64]string{}
	for _, r := range runs {
		sources[r.RunID] = r.Source
	}
	if sources[ghRun.RunID] != SourceGitHubActions {
		t.Errorf("run %d source = %q, want %q", ghRun.RunID, sources[ghRun.RunID], SourceGitHubActions)
	}
	if sources[localRun.RunID] != SourceLocal {
		t.Errorf("run %d source = %q, want %q", localRun.RunID, sources[localRun.RunID], SourceLocal)
	}
}

// openFKEnforcedDB opens a live in-memory database with PRAGMA
// foreign_keys ON.
//
// This is the lane the earlier ci_run_source foreign key never ran in.
// SQLite leaves FK enforcement OFF by default, so a constraint that
// referenced ci_run.run_id -- a non-unique column, since ci_run's key is
// the composite (run_id, repo_id) -- looked fine in every test and would
// have failed the moment a caller turned the pragma on. Opening with it on
// here is what keeps that class of latent break out of this table.
func openFKEnforcedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open fk-enforced test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	var on int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatalf("reading PRAGMA foreign_keys: %v", err)
	}
	if on != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1; this test would otherwise prove nothing", on)
	}
	return db
}

// TestUpsertUnderForeignKeyEnforcement runs the whole local-run write path
// -- reserve, upsert run/job/steps, record the source -- against a
// database with foreign keys ENFORCED, and reads it back.
func TestUpsertUnderForeignKeyEnforcement(t *testing.T) {
	db := openFKEnforcedDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	now := newTestClock().Now()
	repoID := LocalRepoID("/repo/fk")

	runID, err := ReserveLocalRun(ctx, db, repoID, "fk", now)
	if err != nil {
		t.Fatalf("ReserveLocalRun: %v", err)
	}
	run := Run{
		RunID: runID, RepoID: repoID, Name: "fk", Status: RunStatusCompleted,
		Conclusion: ConclusionSuccess, CreatedAt: now, UpdatedAt: now,
	}
	job := Job{JobID: runID, RunID: runID, Name: "local", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	step := Step{JobID: runID, Number: 1, Name: "lint", Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	if err := Upsert(ctx, db, run, []Job{job}, []Step{step}); err != nil {
		t.Fatalf("Upsert under enforced foreign keys: %v", err)
	}
	if err := UpsertRunSource(ctx, db, runID, repoID, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource under enforced foreign keys: %v", err)
	}
	got, err := runSource(ctx, db, runID, repoID)
	if err != nil {
		t.Fatalf("runSource: %v", err)
	}
	if got != SourceLocal {
		t.Errorf("runSource = %q, want %q", got, SourceLocal)
	}
}

// TestUpsertRunSourceForAnAbsentRun states this marker table's contract
// explicitly: with foreign keys ENFORCED, a source row for a run that was
// never written is accepted. That is the documented consequence of having
// no foreign key (see sourceTableStep) -- absence of a run is not an
// integrity error here, and this test is what makes the choice visible
// rather than incidental.
func TestUpsertRunSourceForAnAbsentRun(t *testing.T) {
	db := openFKEnforcedDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	if err := UpsertRunSource(ctx, db, -999, 12345, SourceLocal); err != nil {
		t.Fatalf("UpsertRunSource for an unwritten run: %v", err)
	}
}
