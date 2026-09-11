// Package ci implements the ci_results cascade.db domain (R-16.75):
// ingesting GitHub Actions workflow runs via the REST polling client and
// normalizing them into the ci_run/ci_job/ci_step canonical schema.
//
// Purpose: the ci_results domain's three-table schema (ci_run, ci_job,
//
//	ci_step), authored through the B/S-02.T3 portable migration builder
//	(internal/storage/migrate), following internal/providers/registry/
//	migration.go's and internal/jobs/migration.go's exact precedent, plus
//	the idempotent upsert this domain's own overlapping-poll-pass
//	requirement needs.
//
// DOMAIN REGISTRATION (R-16.75). Unlike registry.go's and jobs/
// migration.go's own domain-registration notes -- both written when the
// R-14.5/R-16.51 set was closed at eleven and neither could claim a
// DomainID -- this package DOES claim one: storage.DomainCIResults, the
// twelfth domain, ratified by T0 ruling R-16.75 specifically for this
// ticket. internal/storage/domains.go carries the constant and the
// AllDomains entry; this file only ever reads storage.DomainCIResults as
// a value, never redeclares it.
//
// SCOPE NOTE (recorded, not papered over): the ticket's task list also
// asks this package to "persist via the C/S-04.T3 event bus + B/S-02
// write executor" and "integrate with the C/S-04.T4 scheduler for
// cadence." Both subsystems exist and are real (internal/events,
// internal/events/scheduler), but this ticket's files_scope.add/change is
// exhaustive and names neither a composition-root file nor an events/
// scheduler file -- exactly the situation internal/providers/registry/
// migration.go's own doc comment already recorded ("wiring it in is left
// to the composition-root ticket that constructs a live Registry").
// domain.go's Upsert is this ticket's own B/S-02-shaped write path
// (a plain *sql.DB write, matching every other domain's CRUD layer in
// this tree -- none of which imports internal/storage/migrate's sibling
// write-executor package either); wiring Upsert's caller (poll.go's
// Client) behind the event bus and scheduler is left to the
// composition-root ticket that constructs a live daemon, matching the
// established precedent above.
//
// SCHEMA VERSION (R-14.198): applied_migrations keys schema_version
// GLOBALLY across cascade.db with no per-MigrationSet identity. Claimed
// slots as of this ticket: bootstrap=1, context/scope=2,
// retrieval/lifecycle=3, providers/registry=4, jobs=5, providers/usage=6,
// conversation=7. This package claims the next unused slot, 8.
//
// SPORT: internal.ci.MigrationSet/ADDED, internal.ci.Upsert/ADDED
//
//	(P1-E25-W5-S51-T2).
package ci

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names. Named exactly as R-16.75 and this ticket's acceptance
// criteria state them ("ci_run, ci_job, ci_step") -- not prefixed with
// the ci_results domain's own TablePrefix, matching the ruling's literal
// wording over the jobs-domain TablePrefix convention it would otherwise
// follow.
const (
	tableRun  = "ci_run"
	tableJob  = "ci_job"
	tableStep = "ci_step"
)

// ciSchemaVersion is this package's MigrationSet target version -- the
// next unused slot in the single global sequence. See this file's SCHEMA
// VERSION doc comment.
const ciSchemaVersion = 8

// SchemaVersion is ciSchemaVersion exported for a future composition
// root's reader-ceiling max(), matching every sibling package's own
// exported-for-the-same-reason pattern.
const SchemaVersion = ciSchemaVersion

// Clock abstracts time.Now (forbidigo, Art.7.3). Declared locally,
// duck-typed, matching internal/storage/domains.go's and
// internal/storage/migrate.Clock's identical pattern.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// MigrationSet is the ci_results domain's three-table schema.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "ci",
		SchemaVersion: ciSchemaVersion,
		ReaderCeiling: ciSchemaVersion,
		Steps: []migrate.MigrationStep{
			runTableStep(),
			jobTableStep(),
			stepTableStep(),
		},
	}
}

func runTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_run: one row per GitHub Actions workflow run, keyed on (run_id, repo_id)",
		Table: &migrate.TableDef{
			Name: tableRun,
			Columns: []migrate.ColumnDef{
				{Name: "run_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "repo_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "name", Type: migrate.TypeText, NotNull: true},
				{Name: "head_branch", Type: migrate.TypeText, NotNull: true},
				{Name: "head_sha", Type: migrate.TypeText, NotNull: true},
				{Name: "status", Type: migrate.TypeText, NotNull: true},
				{Name: "conclusion", Type: migrate.TypeText, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "updated_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

func jobTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_job: one row per job within a run",
		Table: &migrate.TableDef{
			Name: tableJob,
			Columns: []migrate.ColumnDef{
				{Name: "job_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "run_id", Type: migrate.TypeInteger, NotNull: true},
				{Name: "name", Type: migrate.TypeText, NotNull: true},
				{Name: "status", Type: migrate.TypeText, NotNull: true},
				{Name: "conclusion", Type: migrate.TypeText, NotNull: true},
				{Name: "started_at", Type: migrate.TypeInteger},
				{Name: "finished_at", Type: migrate.TypeInteger},
			},
		},
	}
}

func stepTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "ci_step: one row per step within a job",
		Table: &migrate.TableDef{
			Name: tableStep,
			Columns: []migrate.ColumnDef{
				{Name: "job_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "number", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "name", Type: migrate.TypeText, NotNull: true},
				{Name: "status", Type: migrate.TypeText, NotNull: true},
				{Name: "conclusion", Type: migrate.TypeText, NotNull: true},
				{Name: "started_at", Type: migrate.TypeInteger},
				{Name: "finished_at", Type: migrate.TypeInteger},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "job_id", RefTable: tableJob, RefColumn: "job_id", OnDelete: "CASCADE"},
			},
		},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet against db.
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: ApplyMigrationSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        db,
		Dialect:   dialect,
		Clock:     clock,
		DBPath:    dbPath,
		BackupDir: backupDir,
	}, MigrationSet())
}

// Upsert idempotently writes one Run and its Jobs/Steps: overlapping
// polling passes over the same run produce no duplicate rows, keyed on
// (run_id, repo_id) for ci_run and on the job/step's own primary key for
// the other two tables. A single call is not wrapped in an explicit
// transaction across all three tables (matching internal/providers/
// registry's own per-statement CRUD pattern) -- each statement is itself
// atomic and idempotent, so a partial failure leaves no row half-written,
// only some rows not-yet-written, which the next polling pass repairs.
func Upsert(ctx context.Context, db *sql.DB, run Run, jobs []Job, steps []Step) error {
	if err := upsertRun(ctx, db, run); err != nil {
		return err
	}
	for _, j := range jobs {
		if err := upsertJob(ctx, db, j); err != nil {
			return err
		}
	}
	for _, s := range steps {
		if err := upsertStep(ctx, db, s); err != nil {
			return err
		}
	}
	return nil
}

func upsertRun(ctx context.Context, db *sql.DB, r Run) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+tableRun+` (run_id, repo_id, name, head_branch, head_sha, status, conclusion, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, repo_id) DO UPDATE SET
			name=excluded.name, head_branch=excluded.head_branch, head_sha=excluded.head_sha,
			status=excluded.status, conclusion=excluded.conclusion, updated_at=excluded.updated_at`,
		r.RunID, r.RepoID, r.Name, r.HeadBranch, r.HeadSHA, string(r.Status), string(r.Conclusion),
		millis(r.CreatedAt), millis(r.UpdatedAt))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert run %d/%d", r.RunID, r.RepoID)
	}
	return nil
}

func upsertJob(ctx context.Context, db *sql.DB, j Job) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+tableJob+` (job_id, run_id, name, status, conclusion, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			name=excluded.name, status=excluded.status, conclusion=excluded.conclusion,
			started_at=excluded.started_at, finished_at=excluded.finished_at`,
		j.JobID, j.RunID, j.Name, string(j.Status), string(j.Conclusion), millis(j.StartedAt), millis(j.FinishedAt))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert job %d", j.JobID)
	}
	return nil
}

func upsertStep(ctx context.Context, db *sql.DB, s Step) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+tableStep+` (job_id, number, name, status, conclusion, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id, number) DO UPDATE SET
			name=excluded.name, status=excluded.status, conclusion=excluded.conclusion,
			started_at=excluded.started_at, finished_at=excluded.finished_at`,
		s.JobID, s.Number, s.Name, string(s.Status), string(s.Conclusion), millis(s.StartedAt), millis(s.FinishedAt))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "ci: upsert step %d/%d", s.JobID, s.Number)
	}
	return nil
}

// millis renders t as a milliseconds-since-epoch int64, or 0 for the zero
// time.Time (an absent started_at/finished_at on a not-yet-finished job).
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
