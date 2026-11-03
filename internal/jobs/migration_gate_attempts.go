package jobs

// Purpose: independently version the durable gate denial counter schema.
// Inputs: an open jobs database and migration dependencies.
// Outputs: the per-execution counter table.
// Constraints: idempotent migration; existing jobs migrations stay unchanged.
// SPORT: jobs/completion-gate/ADD (P1-PBD-04).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tableGateAttempts = "jobs_gate_attempts"

// GateAttemptsMigrationSet defines the execution-scoped gate denial counters.
func GateAttemptsMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID: "jobs-gate-attempts", SchemaVersion: 1, ReaderCeiling: 1,
		Steps: []migrate.MigrationStep{{
			Kind:        migrate.StepCreateTable,
			Description: "jobs_gate_attempts: durable completion-gate denial counts",
			Table: &migrate.TableDef{
				Name: tableGateAttempts,
				Columns: []migrate.ColumnDef{
					{Name: "execution_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
					{Name: "attempts", Type: migrate.TypeInteger, NotNull: true},
					{Name: "reason", Type: migrate.TypeText, NotNull: true},
				},
				ForeignKeys: []migrate.ForeignKeyDef{
					{Column: "execution_id", RefTable: tableExecution, RefColumn: "id"},
				},
			},
		}},
	}
}

// ApplyGateAttemptsSchema applies the gate counters' independent migration.
func ApplyGateAttemptsSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyGateAttemptsSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: ApplyGateAttemptsSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, GateAttemptsMigrationSet())
}
