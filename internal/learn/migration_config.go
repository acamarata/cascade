package learn

// Purpose: the learned-config schema: five tables in the jobs domain under
//   their own SetID "learn-config" (each feature owns a set id), so they
//   never collide with the "learn" set P1-CAP-02/-03 and P1-TOP-10 own.
// Inputs: none (Go MigrationStep literals, never a numbered .sql file).
// Outputs: ConfigMigrationSet, applied by migrate.Apply at daemon start
//   (P1-LRN-05). SchemaVersion 2 (P1-LRN-02) appends the proposal trigger
//   table, its (trigger_signature, window_end) index and the finding-feed
//   ledger; the ledger re-runs every step at the new version (CREATE ... IF
//   NOT EXISTS), so a version-1 database upgrades by applying the set again.
// Constraints: no alias table (aliases are closed Go data, so no row can
//   repoint one past the denylist); no retention registration (deleting an
//   applied version would break revert). The tables are excluded from
//   storage.Export through storage.JobsDomainExcludedTables.
// SPORT: domain:jobs/jobs_learned_config, jobs_learned_config_submission,
//   jobs_learned_config_version (SetID learn-config v1, P1-LRN-01);
//   jobs_learn_proposal_trigger, jobs_learn_finding_feed (v2, P1-LRN-02).

import "github.com/acamarata/cascade/internal/storage/migrate"

// The three learned-config tables (jobs_ prefix, jobs domain).
const (
	tableLearnedConfig     = "jobs_learned_config"
	tableConfigSubmission  = "jobs_learned_config_submission"
	tableConfigVersion     = "jobs_learned_config_version"
	learnConfigSetID       = "learn-config"
	learnConfigSchemaLevel = 2
)

// The version-2 proposal tables (P1-LRN-02).
const (
	tableProposalTrigger  = "jobs_learn_proposal_trigger"
	tableFindingFeed      = "jobs_learn_finding_feed"
	indexTriggerSignature = "idx_jobs_learn_proposal_trigger_sig_end"
)

// ConfigMigrationSet is the learned-config schema, SetID "learn-config",
// at the current SchemaVersion (2).
func ConfigMigrationSet() migrate.MigrationSet {
	return configMigrationSetAt(learnConfigSchemaLevel)
}

// configMigrationSetAt builds the set as it stood at version (1 or 2): the
// only composite literal of this SetID, so tests can build a genuine
// version-1 database and upgrade it.
func configMigrationSetAt(version int) migrate.MigrationSet {
	steps := []migrate.MigrationStep{learnedConfigTableStep(), submissionTableStep(), versionTableStep()}
	if version >= 2 {
		steps = append(steps, proposalTriggerTableStep(), proposalTriggerIndexStep(), findingFeedTableStep())
	}
	return migrate.MigrationSet{
		SetID:         learnConfigSetID,
		SchemaVersion: version,
		ReaderCeiling: version,
		Steps:         steps,
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

// proposalTriggerTableStep: the detection trigger of one proposal, keyed by
// its learned-config id. Every column is a digest, an opaque id, a count, a
// time or a closed kind; none carries free text.
func proposalTriggerTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_learn_proposal_trigger: the detection trigger of one learned proposal",
		Table: &migrate.TableDef{
			Name: tableProposalTrigger,
			Columns: []migrate.ColumnDef{
				{Name: "config_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				text("trigger_signature"), integer("occurrence_count"), integer("distinct_source_count"),
				text("beneficiary_lane_tier"), text("beneficiary_node_id"),
				integer("window_start"), integer("window_end"), text("change_kind"),
			},
			ForeignKeys: []migrate.ForeignKeyDef{{Column: "config_id", RefTable: tableLearnedConfig, RefColumn: "id"}},
		},
	}
}

// proposalTriggerIndexStep: the per-signature idempotency lookup.
func proposalTriggerIndexStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateIndex,
		Description: "jobs_learn_proposal_trigger (trigger_signature, window_end) lookup",
		Index: &migrate.IndexDef{
			Name: indexTriggerSignature, Table: tableProposalTrigger,
			Columns: []string{"trigger_signature", "window_end"},
		},
	}
}

// findingFeedTableStep: one row per telemetry outcome already fed into
// findings, so the feed is idempotent per outcome.
func findingFeedTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_learn_finding_feed: outcomes already fed into findings",
		Table: &migrate.TableDef{
			Name: tableFindingFeed,
			Columns: []migrate.ColumnDef{
				{Name: "outcome_id", Type: migrate.TypeInteger, PrimaryKey: true, NotNull: true},
				integer("fed_at"),
			},
		},
	}
}
