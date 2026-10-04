package learn

// Purpose: the learn package's own migration set — two tables inside the
//   EXISTING jobs domain (table prefix jobs_), under an independent SetID
//   so P1-E31-W6-S64-T2 can append its own steps and raise the same set
//   to SchemaVersion 2 without colliding with internal/jobs' own "jobs"
//   set or internal/conductor's "conductor-usage" set (R-16.77: the
//   ledger is keyed by (SetID, schema_version), so two sets sharing a
//   version number is not a conflict).
// Inputs: none (the DSL is a Go literal, not caller input).
// Outputs: ordered CREATE TABLE / CREATE INDEX DDL via migrate.Apply.
// Constraints: R-14.306(a) — Go MigrationStep values only, no numbered
//   .sql file and no migrate.Builder (neither exists in this tree).
// SPORT: domain:jobs/jobs_telemetry_outcomes, jobs_telemetry_finding —
//   new tables under SetID learn v1 (P1-E31-W6-S64-T1).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names, prefixed per internal/storage/domains.go's TablePrefix
// convention for the `jobs` domain (this package contributes tables into
// that shared namespace under its own SetID, following internal/conductor/
// usage_migration.go's and internal/evidence's identical precedent).
const (
	tableTelemetryOutcomes = "jobs_telemetry_outcomes"
	tableTelemetryFinding  = "jobs_telemetry_finding"
)

// learnSetID / learnSchemaVersion: see this file's header SetID doc
// comment. P1-E31-W6-S64-T2 appends its own steps and raises
// learnSchemaVersion to 2 in a later change to this same set.
const (
	learnSetID         = "learn"
	learnSchemaVersion = 1
)

// MigrationSet is the learn package's two-table schema.
func MigrationSet() migrate.MigrationSet {
	steps := []migrate.MigrationStep{telemetryOutcomesTableStep()}
	idxSteps := telemetryOutcomeIndexSteps()
	steps = append(steps, idxSteps[:]...)
	steps = append(steps, telemetryFindingTableStep())
	return migrate.MigrationSet{
		SetID:         learnSetID,
		SchemaVersion: learnSchemaVersion,
		ReaderCeiling: learnSchemaVersion,
		Steps:         steps,
	}
}

// telemetryOutcomesTableStep: one row per terminal job, carrying the
// opaque job_id (allowlisted field set — see outcome.go's TelemetryOutcome
// and allowlist.go). id is a surrogate AUTOINCREMENT primary key so
// job_id can carry its OWN unique index (the contract's "carrying the
// opaque job_id" plus "UNIQUE (job_id) among them" five indices), rather
// than job_id itself being the primary key.
func telemetryOutcomesTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_telemetry_outcomes: one allowlisted row per terminal job (R-21.152, R-21.162)",
		Table: &migrate.TableDef{
			Name: tableTelemetryOutcomes,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true, NotNull: true},
				{Name: "job_id", Type: migrate.TypeText, NotNull: true},
				{Name: "task_class", Type: migrate.TypeText, NotNull: true},
				{Name: "repo_id", Type: migrate.TypeText, NotNull: true},
				{Name: "language", Type: migrate.TypeText, NotNull: true},
				{Name: "component", Type: migrate.TypeText, NotNull: true},
				{Name: "risk_class", Type: migrate.TypeText, NotNull: true},
				{Name: "lane_tier", Type: migrate.TypeText, NotNull: true},
				{Name: "node_id", Type: migrate.TypeText, NotNull: true},
				{Name: "scope_ref", Type: migrate.TypeText, NotNull: true},
				{Name: "context_size_tokens", Type: migrate.TypeInteger, NotNull: true},
				{Name: "retrieval_strategy", Type: migrate.TypeText, NotNull: true},
				{Name: "duration_ms", Type: migrate.TypeInteger, NotNull: true},
				{Name: "queue_time_ms", Type: migrate.TypeInteger, NotNull: true},
				{Name: "retry_count", Type: migrate.TypeInteger, NotNull: true},
				{Name: "ci_failure_count", Type: migrate.TypeInteger, NotNull: true},
				{Name: "rework_cycles", Type: migrate.TypeInteger, NotNull: true},
				{Name: "final_outcome", Type: migrate.TypeText, NotNull: true},
				{Name: "rollback_at", Type: migrate.TypeInteger}, // nullable: unset when never rolled back
				{Name: "regression_detected", Type: migrate.TypeInteger, NotNull: true},
				{Name: "cost_tokens", Type: migrate.TypeInteger, NotNull: true},
				{Name: "quota_units", Type: migrate.TypeInteger, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true}, // retention sweep key only, not in the allowlist
			},
		},
	}
}

// telemetryOutcomeIndexSteps: the five indices the contract names —
// UNIQUE(job_id), (node_id), (task_class, lane_tier), (scope_ref), and
// (created_at). DEVIATION: the contract text says "created_at DESC" but
// migrate.IndexDef (dsl.go) has no column-direction field — SQLite/
// Postgres both accept a plain index for range scans either direction, so
// this is a query-planner nicety the DSL cannot express, not a
// correctness gap.
func telemetryOutcomeIndexSteps() [5]migrate.MigrationStep {
	idx := func(name string, unique bool, cols ...string) migrate.MigrationStep {
		return migrate.MigrationStep{
			Kind: migrate.StepCreateIndex,
			Index: &migrate.IndexDef{
				Name: name, Table: tableTelemetryOutcomes, Columns: cols, Unique: unique,
			},
		}
	}
	return [5]migrate.MigrationStep{
		idx("idx_jobs_telemetry_outcomes_job_id", true, "job_id"),
		idx("idx_jobs_telemetry_outcomes_node_id", false, "node_id"),
		idx("idx_jobs_telemetry_outcomes_task_lane", false, "task_class", "lane_tier"),
		idx("idx_jobs_telemetry_outcomes_scope_ref", false, "scope_ref"),
		idx("idx_jobs_telemetry_outcomes_created_at", false, "created_at"),
	}
}

// telemetryFindingTableStep: structured {family, category, severity,
// count} rows over closed enums (finding.go), never free text (R-21.167).
func telemetryFindingTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_telemetry_finding: structured finding rows keyed to one outcome (R-21.167)",
		Table: &migrate.TableDef{
			Name: tableTelemetryFinding,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true, NotNull: true},
				{Name: "outcome_id", Type: migrate.TypeInteger, NotNull: true},
				{Name: "family", Type: migrate.TypeText, NotNull: true},
				{Name: "category", Type: migrate.TypeText, NotNull: true},
				{Name: "severity", Type: migrate.TypeText, NotNull: true},
				{Name: "count", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "outcome_id", RefTable: tableTelemetryOutcomes, RefColumn: "id"},
			},
		},
	}
}

// ApplyLearnSchema idempotently applies MigrationSet against db. Signature
// matches the contract text exactly ("learn.ApplyLearnSchema(ctx, db,
// dialect, clock)") — no dbPath/backupDir snapshot parameters, unlike
// internal/jobs.ApplyJobsSchema's six-arg form; this package's production
// caller (registerLearnJobs) never enables the SQLite pre-migration
// snapshot, matching internal/conductor.ApplyUsageMigrationSchema's own
// "" / "" callers for a non-primary-owner table set.
func ApplyLearnSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: ApplyLearnSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: ApplyLearnSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock,
	}, MigrationSet())
}
