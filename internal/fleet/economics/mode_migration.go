// Purpose: the ONE MigrationSet (B/S-02.T3 typed DSL) that creates
//   policy_scheduler_mode and policy_scheduler_mode_journal inside the
//   EXISTING `policy` storage domain (R-21.22/R-16.51) -- no new domain,
//   no thirteenth DomainID.
//
// Inputs: none. Outputs: a migrate.MigrationSet and ApplyMigrationSchema,
//   the entry point a composition root or test calls, mirroring
//   internal/fleet/topology/migration.go's exact pattern.
//
// Constraints: forward-only, idempotent re-apply, own SchemaVersion/
//   ReaderCeiling (R-16.77 per-SetID identity). CONTRACT DEVIATION: the
//   ticket text describes `incident_renewals INTEGER NOT NULL DEFAULT 0`.
//   The B/S-02.T3 typed DSL's ColumnDef intentionally has no default-value
//   field (dsl.go: "a free-text SQL default is an injection surface... out
//   of scope rather than half-safely supported") -- this file follows the
//   tree: the column is declared INTEGER NOT NULL with no DDL default, and
//   mode_store.go's own writers always supply an explicit 0 rather than
//   relying on one.
//
// SPORT: fleet/economics/mode-migration/ADD (P1-E41-W9-S79-T2).

package economics

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableSchedulerMode and tableSchedulerModeJournal are this package's two
// tables inside the `policy` domain (TablePrefix "policy",
// internal/storage/domains.go).
const (
	tableSchedulerMode        = "policy_scheduler_mode"
	tableSchedulerModeJournal = "policy_scheduler_mode_journal"
)

// modeSchemaVersion is this package's own MigrationSet sequence number,
// independent of every other package's (R-16.77).
const modeSchemaVersion = 1

// schedulerModeTable is the policy_scheduler_mode TableDef: one row per
// project_id (R-21.22), carrying the R-21.118 renewal-cap columns.
func schedulerModeTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableSchedulerMode,
		Columns: []migrate.ColumnDef{
			{Name: "project_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "mode", Type: migrate.TypeText, NotNull: true},
			{Name: "source", Type: migrate.TypeText, NotNull: true},
			{Name: "set_at", Type: migrate.TypeInteger, NotNull: true},
			{Name: "expires_at", Type: migrate.TypeInteger},
			{Name: "incident_renewals", Type: migrate.TypeInteger, NotNull: true},
			{Name: "renewal_window_start", Type: migrate.TypeInteger},
		},
	}
}

// schedulerModeJournalTable is the policy_scheduler_mode_journal
// TableDef: an append-only record of HUMAN_APPROVAL_REQUIRED (and future)
// events, one row per occurrence.
func schedulerModeJournalTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableSchedulerModeJournal,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeInteger, PrimaryKey: true, AutoIncrement: true},
			{Name: "project_id", Type: migrate.TypeText, NotNull: true},
			{Name: "event", Type: migrate.TypeText, NotNull: true},
			{Name: "approval_id", Type: migrate.TypeText, NotNull: true},
			{Name: "recorded_at", Type: migrate.TypeInteger, NotNull: true},
		},
	}
}

// modeTblPtr is a tiny helper so a MigrationStep literal can take the
// address of a table-constructor's return value inline.
func modeTblPtr(t migrate.TableDef) *migrate.TableDef { return &t }

// MigrationSet is fleet economics' scheduler_mode schema, inside the
// EXISTING `policy` storage domain.
func MigrationSet() migrate.MigrationSet {
	modeStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: modeTblPtr(schedulerModeTable()), Description: "policy_scheduler_mode: one row per project, R-21.22/R-21.118"}
	journalStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: modeTblPtr(schedulerModeJournalTable()), Description: "policy_scheduler_mode_journal: append-only HUMAN_APPROVAL_REQUIRED record, R-21.118"}
	return migrate.MigrationSet{
		SetID:         "fleet-economics-mode",
		SchemaVersion: modeSchemaVersion,
		ReaderCeiling: modeSchemaVersion,
		Steps:         []migrate.MigrationStep{modeStep, journalStep},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty.
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: ApplyMigrationSchema requires a non-nil Clock")
	}
	cfg := migrate.ApplyConfig{DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir}
	return migrate.Apply(ctx, cfg, MigrationSet())
}
