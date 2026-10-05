package learn

// Purpose: the learn package's own migration set — four tables inside the
//   EXISTING jobs domain (table prefix jobs_), under an independent SetID
//   so later tickets append steps and raise the same set (SchemaVersion 2
//   here) without colliding with internal/jobs' own "jobs"
//   set or internal/conductor's "conductor-usage" set (R-16.77: the
//   ledger is keyed by (SetID, schema_version), so two sets sharing a
//   version number is not a conflict).
// Inputs: none (the DSL is a Go literal, not caller input).
// Outputs: ordered CREATE TABLE / CREATE INDEX DDL via migrate.Apply.
// Constraints: R-14.306(a) — Go MigrationStep values only, no numbered
//   .sql file and no migrate.Builder (neither exists in this tree).
// SPORT: domain:jobs/jobs_telemetry_outcomes, jobs_telemetry_finding —
//   new tables under SetID learn v1 (P1-E31-W6-S64-T1);
//   jobs_capability_score_observations, jobs_scheduler_decisions — v2 (P1-CAP-03).

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
// comment. SchemaVersion 2 (P1-CAP-03) appends the capability-score and
// scheduler-decision tables after the version-1 steps; the ledger re-runs
// every step at the new version (CREATE ... IF NOT EXISTS), so a database
// already at version 1 upgrades by applying the set again.
const (
	learnSetID         = "learn"
	learnSchemaVersion = 2
)

// Table names the version-2 steps create (jobs_ prefix, same domain).
const (
	tableCapabilityScore   = "jobs_capability_score_observations"
	tableSchedulerDecision = "jobs_scheduler_decisions"
)

// MigrationSet is the learn package's schema at the current SchemaVersion.
func MigrationSet() migrate.MigrationSet { return migrationSetAt(learnSchemaVersion) }

// migrationSetAt builds the set as it stood at version (1 or 2): the only
// composite literal of this SetID, so tests can build a genuine version-1
// database and upgrade it with MigrationSet.
func migrationSetAt(version int) migrate.MigrationSet {
	steps := learnStepsV1()
	if version >= 2 {
		steps = append(steps, learnStepsV2()...)
	}
	return migrate.MigrationSet{
		SetID:         learnSetID,
		SchemaVersion: version,
		ReaderCeiling: version,
		Steps:         steps,
	}
}

// learnStepsV1 is the P1-CAP-02 step list, unchanged and first in every
// later version so the ledger's checksum prefix check still matches.
func learnStepsV1() []migrate.MigrationStep {
	steps := []migrate.MigrationStep{telemetryOutcomesTableStep()}
	idxSteps := telemetryOutcomeIndexSteps()
	steps = append(steps, idxSteps[:]...)
	return append(steps, telemetryFindingTableStep())
}

// learnStepsV2 is the P1-CAP-03 step list: the observation table with its
// unique key, then the decision table with its indices, then two read-path
// indices for the scorer and estimate source: outcomes by (repo_id,
// created_at) and decisions by (selected_tier, task_class, decided_at). The
// outcomes table is the P1-CAP-02 one; an index on it is a V2 step because V1
// is frozen. Later fields of a decision go in a child table
// jobs_scheduler_decision_<name> keyed by decision_id at a higher version
// (the DSL has no ALTER).
func learnStepsV2() []migrate.MigrationStep {
	return []migrate.MigrationStep{
		capabilityScoreTableStep(),
		learnIndexStep("idx_jobs_capability_score_key", tableCapabilityScore, true, "scope_key", "task_class", "tier"),
		schedulerDecisionTableStep(),
		learnIndexStep("idx_jobs_scheduler_decisions_job_id", tableSchedulerDecision, false, "job_id"),
		learnIndexStep("idx_jobs_scheduler_decisions_execution_id", tableSchedulerDecision, false, "execution_id"),
		learnIndexStep("idx_jobs_scheduler_decisions_decided_at", tableSchedulerDecision, false, "decided_at"),
		learnIndexStep("idx_jobs_telemetry_outcomes_repo_created", tableTelemetryOutcomes, false, "repo_id", "created_at"),
		learnIndexStep("idx_jobs_scheduler_decisions_tier_class", tableSchedulerDecision, false, "selected_tier", "task_class", "decided_at"),
	}
}

func learnIndexStep(name, table string, unique bool, cols ...string) migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:  migrate.StepCreateIndex,
		Index: &migrate.IndexDef{Name: name, Table: table, Columns: cols, Unique: unique},
	}
}

// capabilityScoreTableStep: one decayed (alpha, beta) mass per (scope_key,
// task_class, tier). alpha and beta hold OBSERVED successes and failures
// only (decayed to last_updated); the prior is added at read time.
func capabilityScoreTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_capability_score_observations: decayed Beta mass per (scope_key, task_class, tier) (R-16.37)",
		Table: &migrate.TableDef{
			Name: tableCapabilityScore,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true, NotNull: true},
				{Name: "scope_key", Type: migrate.TypeText, NotNull: true},
				{Name: "task_class", Type: migrate.TypeText, NotNull: true},
				{Name: "tier", Type: migrate.TypeText, NotNull: true},
				{Name: "alpha", Type: migrate.TypeReal, NotNull: true},
				{Name: "beta", Type: migrate.TypeReal, NotNull: true},
				{Name: "observation_count", Type: migrate.TypeInteger, NotNull: true},
				{Name: "last_updated", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// schedulerDecisionTableStep: one immutable row per dispatch decision, over
// ids, closed enums and numbers only (R-21.167: no Explain() text, no free
// text column). id is the caller-minted TEXT id the lease can return.
func schedulerDecisionTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_scheduler_decisions: one immutable dispatch decision row (R-21.167)",
		Table: &migrate.TableDef{
			Name: tableSchedulerDecision,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "job_id", Type: migrate.TypeText, NotNull: true},
				{Name: "execution_id", Type: migrate.TypeText, NotNull: true},
				{Name: "task_class", Type: migrate.TypeText, NotNull: true},
				{Name: "selected_tier", Type: migrate.TypeText, NotNull: true},
				{Name: "selected_lane_id", Type: migrate.TypeText, NotNull: true},
				{Name: "selected_node_id", Type: migrate.TypeText, NotNull: true},
				{Name: "score_at_selection", Type: migrate.TypeReal, NotNull: true},
				{Name: "fallback_level", Type: migrate.TypeText, NotNull: true},
				{Name: "jump_rule_fired", Type: migrate.TypeInteger, NotNull: true},
				{Name: "jump_reason_code", Type: migrate.TypeText, NotNull: true},
				{Name: "reserve_tier0_flag", Type: migrate.TypeInteger, NotNull: true},
				{Name: "decided_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
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
