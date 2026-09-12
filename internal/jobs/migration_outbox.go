package jobs

// Purpose: the jobs_outbox table's schema (R-21.148), via the B/S-02.T3
//
//	portable migration builder, following migration_evidence.go's own
//	distinct-SetID precedent (R-16.77: a package may own more than one
//	independently-versioned MigrationSet).
//
// SetID: "jobs" (migration.go) and "jobs-evidence" (migration_evidence.go)
// are already claimed; this file claims the DISTINCT "jobs-outbox" SetID
// at schema_version 1, its own independent sequence under the
// (SetID, schema_version) ledger key -- migration.go's Steps list is
// untouched (out of this ticket's files_scope.change).
//
// SPORT: jobs/scheduler-outbox/ADD (P1-E29-W6-S59-T5).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableOutbox = "jobs_outbox"

const outboxSchemaVersion = 1

// OutboxMigrationSet is the jobs_outbox table's own MigrationSet,
// distinct from MigrationSet() and EvidenceMigrationSet() per R-16.77.
func OutboxMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-outbox",
		SchemaVersion: outboxSchemaVersion,
		ReaderCeiling: outboxSchemaVersion,
		Steps: []migrate.MigrationStep{
			{
				Kind:        migrate.StepCreateTable,
				Description: "jobs_outbox: the R-21.148 transactional outbox for external effects",
				Table: &migrate.TableDef{
					Name: tableOutbox,
					Columns: []migrate.ColumnDef{
						{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
						{Name: "job_id", Type: migrate.TypeText, NotNull: true},
						{Name: "attempt_generation", Type: migrate.TypeInteger, NotNull: true},
						{Name: "site", Type: migrate.TypeText, NotNull: true},
						{Name: "idempotency_key", Type: migrate.TypeText, NotNull: true},
						{Name: "state", Type: migrate.TypeText, NotNull: true},
						{Name: "payload_hash", Type: migrate.TypeText, NotNull: true},
						{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
						{Name: "updated_at", Type: migrate.TypeInteger, NotNull: true},
					},
					ForeignKeys: []migrate.ForeignKeyDef{
						{Column: "job_id", RefTable: tableJob, RefColumn: "id"},
					},
				},
			},
			{
				Kind:        migrate.StepCreateIndex,
				Description: "jobs_outbox: unique index on the R-21.148 stable idempotency key",
				Index: &migrate.IndexDef{
					Name:    "jobs_outbox_idempotency_key_idx",
					Table:   tableOutbox,
					Columns: []string{"idempotency_key"},
					Unique:  true,
				},
			},
		},
	}
}

// ApplyOutboxSchema applies OutboxMigrationSet against db, following
// ApplyEvidenceSchema's exact parameter shape and nil-guards.
func ApplyOutboxSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyOutboxSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyOutboxSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, OutboxMigrationSet())
}
