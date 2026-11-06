package jobs

// Purpose: the jobs_reviewer_decision table's schema, a distinct
// "jobs-reviewer" MigrationSet following migration_outbox.go's pattern
// (R-16.77: a package may own more than one independently-versioned set).
// The table carries no foreign key to jobs_job: decisions are recorded for
// any job id, and the review evidence row references them by
// ReviewEvidenceRef.

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableReviewerDecision = "jobs_reviewer_decision"

const reviewerSchemaVersion = 1

// ReviewerMigrationSet is the jobs_reviewer_decision table's own
// MigrationSet, distinct from every other jobs SetID.
func ReviewerMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-reviewer",
		SchemaVersion: reviewerSchemaVersion,
		ReaderCeiling: reviewerSchemaVersion,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "jobs_reviewer_decision: one row per dispatch or completion review decision",
				Table: &migrate.TableDef{
					Name: tableReviewerDecision,
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
						{Name: "job_id", Type: migrate.TypeText, NotNull: true},
						{Name: "phase", Type: migrate.TypeText, NotNull: true},
						{Name: "risk_class", Type: migrate.TypeText, NotNull: true},
						{Name: "outcome", Type: migrate.TypeText, NotNull: true},
						{Name: "authors", Type: migrate.TypeText, NotNull: true},
						{Name: "reviewer_family", Type: migrate.TypeText, NotNull: true},
						{Name: "reviewer_resolved", Type: migrate.TypeInteger, NotNull: true},
						{Name: "family_distinct", Type: migrate.TypeInteger, NotNull: true},
						{Name: "degraded", Type: migrate.TypeInteger, NotNull: true},
						{Name: "decided_at", Type: migrate.TypeInteger, NotNull: true},
					},
				},
			},
			{
				Kind:        migrate.StepCreateIndex,
				Description: "jobs_reviewer_decision: per-job decision order",
				Index: &migrate.IndexDef{
					Name:    "jobs_reviewer_decision_job_decided_idx",
					Table:   tableReviewerDecision,
					Columns: []string{"job_id", "decided_at"},
				},
			},
		},
	}
}

// ApplyReviewerSchema applies ReviewerMigrationSet against db, with the
// same parameter shape and nil-guards as ApplyOutboxSchema.
func ApplyReviewerSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyReviewerSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyReviewerSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, ReviewerMigrationSet())
}
