package learn

// Purpose: the learned-config schema: three tables in the jobs domain under
//   their own SetID "learn-config" (each feature owns a set id), so they
//   never collide with the "learn" set P1-CAP-02/-03 and P1-TOP-10 own.
// Inputs: none (Go MigrationStep literals, never a numbered .sql file).
// Outputs: ConfigMigrationSet, applied by migrate.Apply at daemon start
//   (P1-LRN-05); P1-LRN-02 raises it to SchemaVersion 2.
// Constraints: no alias table (aliases are closed Go data, so no row can
//   repoint one past the denylist); no retention registration (deleting an
//   applied version would break revert). The tables are excluded from
//   storage.Export through storage.JobsDomainExcludedTables.
// SPORT: domain:jobs/jobs_learned_config, jobs_learned_config_submission,
//   jobs_learned_config_version (SetID learn-config v1, P1-LRN-01).

import "github.com/acamarata/cascade/internal/storage/migrate"

// The three learned-config tables (jobs_ prefix, jobs domain).
const (
	tableLearnedConfig     = "jobs_learned_config"
	tableConfigSubmission  = "jobs_learned_config_submission"
	tableConfigVersion     = "jobs_learned_config_version"
	learnConfigSetID       = "learn-config"
	learnConfigSchemaLevel = 1
)

// ConfigMigrationSet is the learned-config schema, SetID "learn-config",
// SchemaVersion 1.
func ConfigMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         learnConfigSetID,
		SchemaVersion: learnConfigSchemaLevel,
		ReaderCeiling: learnConfigSchemaLevel,
		Steps:         []migrate.MigrationStep{learnedConfigTableStep(), submissionTableStep(), versionTableStep()},
	}
}

// text and integer build NOT NULL columns; the nullable ones are spelled out.
func text(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeText, NotNull: true}
}

func integer(name string) migrate.ColumnDef {
	return migrate.ColumnDef{Name: name, Type: migrate.TypeInteger, NotNull: true}
}

// learnedConfigTableStep: one row per learned proposal, tier as computed.
func learnedConfigTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_learned_config: one row per learned proposal with its computed tier",
		Table: &migrate.TableDef{
			Name: tableLearnedConfig,
			Columns: []migrate.ColumnDef{
				{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("target_id"), text("scope_ref"), text("label"),
				{Name: "confidence", Type: migrate.TypeReal, NotNull: true},
				text("tier"), text("areas"), integer("loosens_bound"),
				text("source_kind"), text("source_ref"),
				integer("created_at"), integer("last_verified_at"),
				integer("success_count"), integer("failure_count"), integer("version"),
			},
		},
	}
}

// submissionTableStep: the proposal payload and its status, one per config.
func submissionTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_learned_config_submission: change, evidence and status of one proposal",
		Table: &migrate.TableDef{
			Name: tableConfigSubmission,
			Columns: []migrate.ColumnDef{
				{Name: "config_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("change_json"), text("evidence_json"), text("status"),
				integer("submitted_at"), integer("status_changed_at"),
			},
			ForeignKeys: []migrate.ForeignKeyDef{{Column: "config_id", RefTable: tableLearnedConfig, RefColumn: "id"}},
		},
	}
}

// versionTableStep: the applied-value history; revert appends, never deletes.
func versionTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_learned_config_version: append-only applied-value history",
		Table: &migrate.TableDef{
			Name: tableConfigVersion,
			Columns: []migrate.ColumnDef{
				{Name: "config_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "version", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				text("value"),
				{Name: "baseline_metric", Type: migrate.TypeReal},
				integer("applied_at"), text("applied_by"),
				{Name: "reverted_at", Type: migrate.TypeInteger},
				text("revert_reason"),
			},
			ForeignKeys: []migrate.ForeignKeyDef{{Column: "config_id", RefTable: tableLearnedConfig, RefColumn: "id"}},
		},
	}
}
