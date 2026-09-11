package jobs

// Purpose: the jobs domain's seven-table schema (R-14.5/R-16.12/R-16.13),
//
//	authored through the B/S-02.T3 portable migration builder
//	(internal/storage/migrate), following internal/context/scope/
//	schema.go's and internal/providers/registry/migration.go's exact
//	precedent.
//
// SCHEMA VERSION (R-14.198): applied_migrations keys schema_version
// GLOBALLY across cascade.db with no per-MigrationSet identity (see
// internal/context/scope/schema.go's doc comment for the full
// discovery). Claimed slots as of this ticket: bootstrap=1,
// context/scope=2, retrieval/lifecycle=3, providers/registry=4. This
// package claims the next unused slot, 5.
//
// COMPOSITE-FK LIMITATION (contract/tree contradiction, both sides
// quoted in the journal): the DECIDED Worktree record binds to its
// ResourceLease by (repo id, scope_glob) -- ResourceLease's own natural,
// DECIDED composite key. migrate.ForeignKeyDef supports only a single
// local/referenced column pair (dsl.go's ForeignKeyDef has exactly one
// Column/RefTable/RefColumn triple), so a genuine composite FOREIGN KEY
// cannot be expressed through this ticket's mandated tooling. worktree
// therefore carries lease_repo_id/lease_scope_glob as plain NOT NULL
// columns with no DB-level FK; store_lease.go enforces the binding at
// the application layer (WorktreeStore.Put refuses a lease ref that
// does not resolve to an existing resource_lease row).
//
// SPORT: internal.jobs.MigrationSet/ADDED (P1-E29-W6-S59-T1).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names, prefixed per storage/domains.go's TablePrefix convention
// for the `jobs` domain.
const (
	tableJob             = "jobs_job"
	tableTaskDependency  = "jobs_task_dependency"
	tableExecution       = "jobs_execution"
	tableExecutionResult = "jobs_execution_result"
	tableArtifact        = "jobs_artifact"
	tableResourceLease   = "jobs_resource_lease"
	tableWorktree        = "jobs_worktree"
)

// jobsSchemaVersion is this package's MigrationSet target version -- the
// next unused slot in the single global sequence. See this file's SCHEMA
// VERSION doc comment.
const jobsSchemaVersion = 5

// SchemaVersion is jobsSchemaVersion exported for a future composition
// root's reader-ceiling max(), matching scope.SchemaVersion's,
// lifecycle.SchemaVersion's and registry.SchemaVersion's own
// exported-for-the-same-reason pattern. Not yet wired into
// cmd/cascade/daemon_unix_store.go's runtimeReaderCeiling -- see
// testonly-allow.json (this ticket's files_scope.change lists only
// internal/storage/domains.go, matching registry's own precedent for the
// identical situation).
const SchemaVersion = jobsSchemaVersion

// MigrationSet is the jobs domain's seven-table schema.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        jobsSchemaVersion,
		MinimumReaderVersion: jobsSchemaVersion,
		Steps: []migrate.MigrationStep{
			jobTableStep(),
			taskDependencyTableStep(),
			executionTableStep(),
			executionResultTableStep(),
			artifactTableStep(),
			resourceLeaseTableStep(),
			worktreeTableStep(),
		},
	}
}

func jobTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_job: one row per DAG-node job (R-16.37 state machine)",
		Table: &migrate.TableDef{
			Name: tableJob,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "updated_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "capabilities", Type: migrate.TypeText, NotNull: true},
				{Name: "mutable_scope", Type: migrate.TypeText, NotNull: true},
				{Name: "risk_class", Type: migrate.TypeText, NotNull: true},
				{Name: "min_task_class", Type: migrate.TypeText, NotNull: true},
				{Name: "node_requirements", Type: migrate.TypeText, NotNull: true},
				{Name: "timeout_seconds", Type: migrate.TypeInteger, NotNull: true},
				{Name: "cost_ceiling", Type: migrate.TypeReal, NotNull: true},
				{Name: "priority", Type: migrate.TypeInteger, NotNull: true},
				// consecutive_failed_attempts: contract text says
				// "INTEGER NOT NULL DEFAULT 0" (R-21.84); the DSL has no
				// DEFAULT support (dsl.go's ColumnDef doc comment: "out of
				// scope rather than half-safely supported"). NOT NULL is
				// enforced here; the 0 default is application-layer, at
				// store_job.go's PutJob (see this file's package doc and
				// the journal for the full quoted contradiction).
				{Name: "consecutive_failed_attempts", Type: migrate.TypeInteger, NotNull: true},
				{Name: "consequence_class", Type: migrate.TypeText, NotNull: true},
				{Name: "data_class", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

func taskDependencyTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_task_dependency: deps[] edges materialized as rows",
		Table: &migrate.TableDef{
			Name: tableTaskDependency,
			Columns: []migrate.ColumnDef{
				{Name: "job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "depends_on_job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "job_id", RefTable: tableJob, RefColumn: "id"},
				{Column: "depends_on_job_id", RefTable: tableJob, RefColumn: "id"},
			},
		},
	}
}

func executionTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_execution: one row per attempt at a job (R-16.33)",
		Table: &migrate.TableDef{
			Name: tableExecution,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "job_id", Type: migrate.TypeText, NotNull: true},
				{Name: "attempt", Type: migrate.TypeInteger, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
				{Name: "started_at", Type: migrate.TypeInteger},
				{Name: "ended_at", Type: migrate.TypeInteger},
				{Name: "pgid", Type: migrate.TypeInteger},
				{Name: "heartbeat_at", Type: migrate.TypeInteger},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "job_id", RefTable: tableJob, RefColumn: "id"},
			},
		},
	}
}

func executionResultTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_execution_result: one outcome row per execution",
		Table: &migrate.TableDef{
			Name: tableExecutionResult,
			Columns: []migrate.ColumnDef{
				{Name: "execution_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "output_summary", Type: migrate.TypeText, NotNull: true},
				{Name: "error_kind", Type: migrate.TypeText},
				{Name: "error_message", Type: migrate.TypeText},
				{Name: "artifact_refs", Type: migrate.TypeText, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "execution_id", RefTable: tableExecution, RefColumn: "id"},
			},
		},
	}
}

func artifactTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_artifact: one row per produced file/blob (BLAKE3-keyed)",
		Table: &migrate.TableDef{
			Name: tableArtifact,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "job_id", Type: migrate.TypeText, NotNull: true},
				{Name: "execution_id", Type: migrate.TypeText, NotNull: true},
				{Name: "kind", Type: migrate.TypeText, NotNull: true},
				{Name: "blob_key", Type: migrate.TypeText, NotNull: true},
				{Name: "path", Type: migrate.TypeText, NotNull: true},
				{Name: "consequence_class", Type: migrate.TypeText, NotNull: true},
				{Name: "data_class", Type: migrate.TypeText, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "job_id", RefTable: tableJob, RefColumn: "id"},
				{Column: "execution_id", RefTable: tableExecution, RefColumn: "id"},
			},
		},
	}
}

func resourceLeaseTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_resource_lease: the DECIDED one-mutable-writer lease record",
		Table: &migrate.TableDef{
			Name: tableResourceLease,
			Columns: []migrate.ColumnDef{
				{Name: "repo_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "scope_glob", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "holder", Type: migrate.TypeText, NotNull: true},
				{Name: "issued_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "ttl_seconds", Type: migrate.TypeInteger, NotNull: true},
				{Name: "renew_count", Type: migrate.TypeInteger, NotNull: true},
				{Name: "journal_ref", Type: migrate.TypeText, NotNull: true},
				{Name: "epoch", Type: migrate.TypeInteger, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

func worktreeTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_worktree: one checked-out working tree per lease-bound job",
		Table: &migrate.TableDef{
			Name: tableWorktree,
			Columns: []migrate.ColumnDef{
				{Name: "path", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				// lease_repo_id/lease_scope_glob: no DB-level FK, see this
				// file's COMPOSITE-FK LIMITATION doc comment.
				{Name: "lease_repo_id", Type: migrate.TypeText, NotNull: true},
				{Name: "lease_scope_glob", Type: migrate.TypeText, NotNull: true},
				{Name: "repo", Type: migrate.TypeText, NotNull: true},
				{Name: "branch", Type: migrate.TypeText, NotNull: true},
			},
		},
	}
}

// ApplyJobsSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty.
func ApplyJobsSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyJobsSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyJobsSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        db,
		Dialect:   dialect,
		Clock:     clock,
		DBPath:    dbPath,
		BackupDir: backupDir,
	}, MigrationSet())
}
