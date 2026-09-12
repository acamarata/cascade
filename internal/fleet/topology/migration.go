// Purpose: the ONE MigrationSet (B/S-02.T3 builder) that creates account,
//
//	credential, quota_domain, runtime_profile and lane inside the
//	EXISTING `config` storage domain (R-21.22) -- no new domain, no
//	twelfth DomainID.
//
// Inputs: none. Outputs: a migrate.MigrationSet and ApplyMigrationSchema,
//
//	the entry point a composition root or test calls.
//
// Constraints: forward-only, idempotent re-apply, carries its own
//
//	SchemaVersion/ReaderCeiling (R-16.77 gives per-SetID identity, so this
//	number is independent of every other package's own sequence). The
//	contract text names a field "minimum_reader_version"; R-16.77 renamed
//	that concept ReaderCeiling tree-wide (see this ticket's journal for
//	the full contract-vs-tree note) -- this file follows the tree.
//
// SPORT: fleet/topology/migration/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// topologySchemaVersion is this package's own MigrationSet sequence
// number, independent of every other package's (R-16.77).
const topologySchemaVersion = 1

// SchemaVersion is topologySchemaVersion, exported for a future
// composition root's reader-ceiling max(), matching registry.SchemaVersion's
// and scope.SchemaVersion's own pattern.
const SchemaVersion = topologySchemaVersion

// MigrationSet is fleet topology's five-table schema, inside the `config`
// storage domain.
func MigrationSet() migrate.MigrationSet {
	accountStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(accountTable()), Description: "config_account: one row per provider account (R-21.24)"}
	quotaStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(quotaDomainTable()), Description: "config_quota_domain: one row per (account, billing kind) quota domain (R-21.24)"}
	profileStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(runtimeProfileTable()), Description: "config_runtime_profile: one row per (provider, runtime) pair (R-21.24)"}
	credStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(credentialTable()), Description: "config_credential: one row per lane record's credential, secret_ref only (R-21.24, R-16.44)"}
	laneStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(laneTable()), Description: "config_lane: one row per lane, derived id, model_identity + offering_snapshot columns (R-21.24, R-21.72, R-21.101)"}

	return migrate.MigrationSet{
		SetID:         "fleet-topology",
		SchemaVersion: topologySchemaVersion,
		ReaderCeiling: topologySchemaVersion,
		Steps:         []migrate.MigrationStep{accountStep, quotaStep, profileStep, credStep, laneStep},
	}
}

// tblPtr is a tiny helper so each MigrationStep literal above can take the
// address of a table-constructor's return value inline.
func tblPtr(t migrate.TableDef) *migrate.TableDef { return &t }

// quotaSchemaVersion is the quota-tables MigrationSet's own sequence
// number (R-16.77 per-SetID identity), independent of topologySchemaVersion.
const quotaSchemaVersion = 1

// QuotaMigrationSet is the S-77.T3 schema: sessions_quota_bucket (a
// READ-ONLY observation cache, R-21.114) and sessions_quota_snapshot
// (append-only), both in the EXISTING `sessions` storage domain (R-21.22 --
// table-prefix convention only, no new DomainID). A distinct SetID from
// MigrationSet's "fleet-topology" so the two schemas apply and re-apply
// independently in the shared ledger.
func QuotaMigrationSet() migrate.MigrationSet {
	bucketStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(quotaBucketTable()), Description: "sessions_quota_bucket: read-only per-(domain,dimension) observation cache (R-21.26, R-21.96, R-21.114)"}
	snapshotStep := migrate.MigrationStep{Kind: migrate.StepCreateTable, Table: tblPtr(quotaSnapshotTable()), Description: "sessions_quota_snapshot: append-only fleet.quota.snapshot rows (R-21.26)"}
	return migrate.MigrationSet{
		SetID:         "fleet-topology-quota",
		SchemaVersion: quotaSchemaVersion,
		ReaderCeiling: quotaSchemaVersion,
		Steps:         []migrate.MigrationStep{bucketStep, snapshotStep},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet and
// QuotaMigrationSet against db. dbPath/backupDir enable migrate's SQLite
// snapshot when non-empty.
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "topology: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "topology: ApplyMigrationSchema requires a non-nil Clock")
	}
	cfg := migrate.ApplyConfig{DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir}
	if err := migrate.Apply(ctx, cfg, MigrationSet()); err != nil {
		return err
	}
	return migrate.Apply(ctx, cfg, QuotaMigrationSet())
}
