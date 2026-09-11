package evidence

// Purpose: the jobs-domain jobs_claim and jobs_claim_evidence tables'
// schema, authored through the B/S-02.T3 portable migration builder
// (internal/storage/migrate), following internal/jobs/migration.go's and
// migration_evidence.go's own precedent.
//
// TABLE NAMES: R-21.51 names the two tables "claim" and "claim_evidence"
// informally; internal/storage/domains.go's TablePrefix convention (every
// sibling jobs-domain table -- jobs_job, jobs_execution, jobs_evidence,
// jobs_artifact, jobs_resource_lease, jobs_worktree -- is prefixed
// "jobs_") is binding infrastructure this ticket follows rather than
// deviates from: the tables are jobs_claim and jobs_claim_evidence, same
// row identity R-21.51 names, correctly namespaced within the domain like
// every table beside them.
//
// SetID (R-16.77): distinct from "jobs" (migration.go) and "jobs-evidence"
// (migration_evidence.go) -- this file claims "jobs-claim-evidence" at its
// own independent schema_version 1.
//
// SPORT: evidence/claim-record (ADD).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	tableClaim         = "jobs_claim"
	tableClaimEvidence = "jobs_claim_evidence"
)

const claimSchemaVersion = 1

// MigrationSet is the jobs_claim/jobs_claim_evidence schema, distinct from
// jobs.MigrationSet() and jobs.EvidenceMigrationSet() per R-16.77.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "jobs-claim-evidence",
		SchemaVersion: claimSchemaVersion,
		ReaderCeiling: claimSchemaVersion,
		Steps: []migrate.MigrationStep{
			claimTableStep(),
			claimEvidenceTableStep(),
			claimRunIndexStep(),
			claimEvidenceClaimIndexStep(),
		},
	}
}

func claimTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_claim: R-21.41 claim record plus R-21.94 immutable data_class",
		Table: &migrate.TableDef{
			Name: tableClaim,
			Columns: []migrate.ColumnDef{
				{Name: "claim_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "statement", Type: migrate.TypeText, NotNull: true},
				{Name: "type", Type: migrate.TypeText, NotNull: true},
				{Name: "confidence", Type: migrate.TypeReal, NotNull: true},
				{Name: "data_class", Type: migrate.TypeText, NotNull: true},
				{Name: "run_id", Type: migrate.TypeText, NotNull: true},
				{Name: "lane_id", Type: migrate.TypeText, NotNull: true},
				{Name: "verification_json", Type: migrate.TypeText, NotNull: true},
				{Name: "contradictions_json", Type: migrate.TypeText, NotNull: true},
				// invalidated_at: 0 means nil (unset) -- the later-
				// invalidation flag stands while this column reads 0.
				{Name: "invalidated_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

func claimEvidenceTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_claim_evidence: R-21.41 evidence record plus R-21.94 data_class and R-21.86 captured_ref",
		Table: &migrate.TableDef{
			Name: tableClaimEvidence,
			Columns: []migrate.ColumnDef{
				{Name: "evidence_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "claim_id", Type: migrate.TypeText, NotNull: true},
				{Name: "source_type", Type: migrate.TypeText, NotNull: true},
				{Name: "data_class", Type: migrate.TypeText, NotNull: true},
				{Name: "repository", Type: migrate.TypeText, NotNull: true},
				{Name: "commit", Type: migrate.TypeText, NotNull: true},
				{Name: "path", Type: migrate.TypeText, NotNull: true},
				{Name: "locator", Type: migrate.TypeText, NotNull: true},
				{Name: "content_hash", Type: migrate.TypeText, NotNull: true},
				{Name: "captured_ref", Type: migrate.TypeText, NotNull: true},
				{Name: "observed_at", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "claim_id", RefTable: tableClaim, RefColumn: "claim_id"},
			},
		},
	}
}

func claimRunIndexStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateIndex,
		Description: "jobs_claim(run_id)",
		Index: &migrate.IndexDef{
			Name:    "idx_jobs_claim_run_id",
			Table:   tableClaim,
			Columns: []string{"run_id"},
		},
	}
}

func claimEvidenceClaimIndexStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateIndex,
		Description: "jobs_claim_evidence(claim_id)",
		Index: &migrate.IndexDef{
			Name:    "idx_jobs_claim_evidence_claim_id",
			Table:   tableClaimEvidence,
			Columns: []string{"claim_id"},
		},
	}
}

// ApplySchema applies MigrationSet against db. Re-apply is an idempotent
// no-op (migrate.Apply's own forward-only guarantee).
func ApplySchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "evidence: ApplySchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "evidence: ApplySchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, MigrationSet())
}
