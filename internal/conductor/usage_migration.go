// Purpose: the jobs-domain jobs_usage table's schema (R-16.52), authored
//   through the B/S-02.T3 portable migration builder
//   (internal/storage/migrate), following internal/evidence/
//   migration_claim.go's precedent for a package OTHER than internal/jobs
//   (the jobs domain's own owner) contributing a table into the shared
//   jobs_ table-prefix namespace under its own independent SetID
//   (internal/storage/domains.go's TablePrefix convention is binding
//   infrastructure this ticket follows, not a new domain registration -
//   R-16.52/R-14.5): the table is jobs_usage, not conductor_usage.
//
// SetID (R-16.77): "conductor-usage", distinct from "jobs" (internal/
//   jobs/migration.go), "jobs-evidence", "jobs-outbox" and
//   "jobs-claim-evidence" (internal/jobs/*.go, internal/evidence/*.go) -
//   independently versioned, per R-16.77's "two MigrationSets with
//   different SetID values may freely share a SchemaVersion" rule.
//
// CONTRACT NOTE (R-16.79, both sides quoted): full_desc's task 3 says
// "Register the usage table migration ... via internal/conductor/
// usage_migration.go" with no further instruction on how a migration
// authored in a package OTHER than the domain owner reaches the daemon's
// real cascade.db. The tree's own worked precedent for exactly this
// situation (internal/evidence contributing jobs_claim/jobs_claim_evidence
// under the jobs_ prefix) wires its ApplySchema call from a NEW
// cmd/cascade/daemon_unix_evidence.go file - which was in THAT ticket's
// own files_scope.add. This ticket's files_scope has no cmd/ or
// internal/daemon/ entry (files_scope.add lists only internal/conductor/
// usage.go, usage_test.go, usage_migration.go, usage_migration_test.go),
// so ApplyUsageMigrationSchema and NewUsageStore have no path to a
// production caller from inside this ticket - the same situation
// spawn_hook.go's NewDefaultProductionSpawnHook already documents and
// tracks in internal/build/testonly-allow.json against P1-E11-W3-S22-T4
// (the ticket that constructs a real Executor at the daemon composition
// root). This ticket's own two new allow-list entries cite the same
// ticket for the same reason: the daemon composition root that would open
// cascade.db, call ApplyUsageMigrationSchema, construct a *UsageStore via
// NewUsageStore, and wire both onto the Executor via SetUsageStore/
// SetUsageAggregator does not exist until that ticket lands.
//
// Inputs: WriteUsageRecord takes a fully-populated UsageRecord (usage.go);
//   UpdateOutcomeClass takes a JobID and the new outcome_class string.
// Outputs: jobs_usage rows; a pkg/cascade taxonomy error (KindNotFound for
//   UpdateOutcomeClass on an unknown job_id, KindUnavailable for a
//   transport failure).
// Constraints: WriteUsageRecord issues exactly one INSERT per call (task 3:
//   "this ticket never updates a row after writing it") - AE/S-64.T1's
//   UpdateOutcomeClass is the only path that ever mutates an existing row,
//   and only its outcome_class column.
// SPORT: conductor.usage-attribution/ADD (P1-E11-W3-S23-T4).

package conductor

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableUsage is the jobs_usage table name, prefixed per internal/storage/
// domains.go's TablePrefix convention for the jobs domain (matching
// jobs_job, jobs_execution, jobs_claim, ... - never a bare "usage" name).
const tableUsage = "jobs_usage"

// usageSetID / usageSchemaVersion: see this file's header SetID doc
// comment.
const (
	usageSetID         = "conductor-usage"
	usageSchemaVersion = 1
)

// usageMigrationSet is the jobs_usage schema: one table, PK job_id, no
// foreign key (job_id correlates to the jobs domain's own job records by
// convention, not a DB-level FK - the jobs_usage row must survive
// independently of jobs_job's own lifecycle for AE/S-64.T1's later read).
func usageMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         usageSetID,
		SchemaVersion: usageSchemaVersion,
		ReaderCeiling: usageSchemaVersion,
		Steps: []migrate.MigrationStep{
			usageTableStep(),
		},
	}
}

func usageTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_usage: one row per terminal conductor.Execute outcome (R-16.52)",
		Table: &migrate.TableDef{
			Name: tableUsage,
			Columns: []migrate.ColumnDef{
				{Name: "job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "lane_id", Type: migrate.TypeText, NotNull: true},
				{Name: "task_class", Type: migrate.TypeText, NotNull: true},
				{Name: "tokens_in", Type: migrate.TypeInteger, NotNull: true},
				{Name: "tokens_out", Type: migrate.TypeInteger, NotNull: true},
				{Name: "cost_micros", Type: migrate.TypeInteger, NotNull: true},
				{Name: "wall_ms", Type: migrate.TypeInteger, NotNull: true},
				{Name: "outcome_class", Type: migrate.TypeText, NotNull: true},
				{Name: "attempt", Type: migrate.TypeInteger, NotNull: true},
				{Name: "requesting_entity", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

// ApplyUsageMigrationSchema idempotently applies usageMigrationSet against
// db. dbPath/backupDir enable migrate.Apply's §D-18 pre-migration snapshot
// (SQLite only); pass "" for both against Postgres or an in-memory test
// database, matching every sibling ApplyXSchema function's own contract
// (internal/jobs.ApplyJobsSchema, internal/evidence.ApplySchema).
func ApplyUsageMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "conductor: ApplyUsageMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "conductor: ApplyUsageMigrationSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        db,
		Dialect:   dialect,
		Clock:     clock,
		DBPath:    dbPath,
		BackupDir: backupDir,
	}, usageMigrationSet())
}

// UsageStore is the per-job jobs_usage write surface UsageRecorder names.
// The zero value is not usable; construct with NewUsageStore against a db
// already migrated via ApplyUsageMigrationSchema.
type UsageStore struct {
	db *sql.DB
}

// NewUsageStore returns a UsageStore persisting through db. See this
// file's header CONTRACT NOTE for why nothing in this ticket's files_scope
// constructs one in production yet.
func NewUsageStore(db *sql.DB) *UsageStore {
	return &UsageStore{db: db}
}

var _ UsageRecorder = (*UsageStore)(nil)

// ErrUsageRecordNotFound is UpdateOutcomeClass's typed refusal for an
// unknown job_id (task 3's "not-found typed error on unknown job_id").
// Wraps the frozen KindNotFound rather than inventing a fifteenth kind.
var ErrUsageRecordNotFound = cascade.New(cascade.KindNotFound, "conductor: no jobs_usage row for job_id")

// WriteUsageRecord inserts exactly one jobs_usage row for r.JobID. It
// never updates an existing row (task 3) - a second call for the same
// JobID surfaces whatever constraint error the driver returns, which
// attributeUsage (usage.go) treats as any other write failure: logged at
// warn, never propagated to Execute's caller.
func (s *UsageStore) WriteUsageRecord(ctx context.Context, r UsageRecord) error {
	if s == nil || s.db == nil {
		return cascade.New(cascade.KindInvalidInput, "conductor: WriteUsageRecord requires a constructed UsageStore")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+tableUsage+`
			(job_id, lane_id, task_class, tokens_in, tokens_out, cost_micros, wall_ms, outcome_class, attempt, requesting_entity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(r.JobID), r.LaneID, r.TaskClass, r.TokensIn, r.TokensOut, r.CostMicroUSD, r.WallMS, r.OutcomeClass, r.Attempt, r.RequestingEntity)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conductor: write jobs_usage row")
	}
	return nil
}

// UpdateOutcomeClass sets ONLY the outcome_class column of the existing
// jobs_usage row for jobID (AE/S-64.T1's later learned-outcome write).
// Returns ErrUsageRecordNotFound, writing nothing, when jobID has no row.
func (s *UsageStore) UpdateOutcomeClass(ctx context.Context, jobID JobID, outcomeClass string) error {
	if s == nil || s.db == nil {
		return cascade.New(cascade.KindInvalidInput, "conductor: UpdateOutcomeClass requires a constructed UsageStore")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableUsage+` SET outcome_class = ? WHERE job_id = ?`,
		outcomeClass, string(jobID))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conductor: update jobs_usage outcome_class")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conductor: update jobs_usage outcome_class: rows affected")
	}
	if n == 0 {
		return ErrUsageRecordNotFound
	}
	return nil
}
