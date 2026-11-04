package jobs

// Purpose: the jobs_completion_approval table's schema (P1-PBD-09), via
//
//	the portable migration builder, following migration_outbox.go's
//	distinct-SetID precedent (R-16.77: a package may own more than one
//	independently-versioned MigrationSet).
//
// SetID: "jobs", "jobs-evidence" and "jobs-outbox" are already claimed;
// this file claims the DISTINCT "jobs-completion-approval" SetID at
// schema_version 1. One row per (job_id, action): the composite primary
// key is the store's natural key.

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableCompletionApproval = "jobs_completion_approval"

const completionApprovalSchemaVersion = 1

// CompletionApprovalMigrationSet is the jobs_completion_approval table's
// own MigrationSet, distinct from the other jobs sets per R-16.77.
func CompletionApprovalMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-completion-approval",
		SchemaVersion: completionApprovalSchemaVersion,
		ReaderCeiling: completionApprovalSchemaVersion,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "jobs_completion_approval: one held completion approval per (job, action)",
				Table: &migrate.TableDef{
					Name: tableCompletionApproval,
					Columns: []migrate.ColumnDef{
						{Name: "job_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
						{Name: "action", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
						{Name: "nonce", Type: migrate.TypeText, NotNull: true},
						{Name: "policy_version", Type: migrate.TypeText, NotNull: true},
						{Name: "denied", Type: migrate.TypeInteger, NotNull: true},
						{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
					},
				},
			},
		},
	}
}

// ApplyCompletionApprovalSchema applies CompletionApprovalMigrationSet
// against db, following ApplyOutboxSchema's parameter shape and nil-guards.
func ApplyCompletionApprovalSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyCompletionApprovalSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyCompletionApprovalSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, CompletionApprovalMigrationSet())
}
