// Purpose: the usage accounting domain's on-disk schema -- one real
//   relational table, provider_usage, authored through the B/S-02.T3
//   portable migration builder (internal/storage/migrate), exactly as
//   internal/providers/registry/migration.go does for the sibling S-20.T2
//   registry.
//
// CONTRACT DEVIATION (recorded, mirrors registry/migration.go's own
// identically-reasoned note). The ticket's task-2 wording says "Register
// the 'usage' domain via B/S-03.T1 domain layout API", but
// internal/storage/domains.go's AllDomains is CLOSED at eleven members
// (R-14.5, amended once by R-16.51 to add `policy`); there is no twelfth
// slot and this ticket has no standing to add one -- the exact situation
// internal/providers/registry, internal/fleet/sessions/domain.go, and
// internal/retrieval/lifecycle/migrate.go have each already hit and
// recorded identically. This package therefore does NOT call
// storage.Bootstrap or claim a DomainID; it authors its own
// migrate.MigrationSet against cascade.db's raw *sql.DB.
//
// SCHEMA VERSION (R-14.198). applied_migrations keys schema_version
// GLOBALLY with no per-MigrationSet identity. The claimed slots in the
// tree as of this ticket: bootstrap=1, context/scope=2,
// retrieval/lifecycle=3, providers/registry=4, jobs=5. This package
// claims the next unused slot, 6.
//
// Inputs: none at this layer.
// Outputs: a migrate.MigrationSet / ApplyMigrationSchema.
// Constraints: idempotent re-apply (migrate.Apply's own contract);
//   minimum_reader_version equals schema_version, matching registry's own
//   choice (no reader compatibility window needed for a brand-new table).
// SPORT: provider.usage/ADD (P1-E10-W3-S20-T4).

package usage

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableProviderUsage is this domain's sole table.
const tableProviderUsage = "provider_usage"

// usageSchemaVersion is this package's MigrationSet target version -- the
// next unused slot in the single global sequence (see this file's SCHEMA
// VERSION doc comment).
const usageSchemaVersion = 6

// SchemaVersion is usageSchemaVersion exported for a future composition
// root's reader-ceiling max(), matching registry.SchemaVersion's own
// exported-for-the-same-reason pattern.
const SchemaVersion = usageSchemaVersion

// MigrationSet is the usage accounting domain's one-table schema. The
// composite primary key (provider_name, lane_name, model_name, bucket)
// is what makes IncrementUsage's upsert idempotent per the ticket's
// COST CALCULATION/WRITE INTERFACE contract.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        usageSchemaVersion,
		MinimumReaderVersion: usageSchemaVersion,
		Steps:                []migrate.MigrationStep{providerUsageTableStep()},
	}
}

func providerUsageTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "provider_usage: per (provider, lane, model, day-bucket) token/cost counters",
		Table: &migrate.TableDef{
			Name: tableProviderUsage,
			Columns: []migrate.ColumnDef{
				{Name: "provider_name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "lane_name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "model_name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "bucket", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "tokens_in", Type: migrate.TypeInteger, NotNull: true},
				{Name: "tokens_out", Type: migrate.TypeInteger, NotNull: true},
				{Name: "requests", Type: migrate.TypeInteger, NotNull: true},
				{Name: "errors", Type: migrate.TypeInteger, NotNull: true},
				{Name: "cost_micro_usd", Type: migrate.TypeInteger, NotNull: true},
				{Name: "last_used_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty.
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "usage: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "usage: ApplyMigrationSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB: db, Dialect: dialect, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	}, MigrationSet())
}
