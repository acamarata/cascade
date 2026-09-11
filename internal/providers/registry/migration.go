// Purpose: the providers registry's on-disk schema -- two real relational
//
//	tables, provider_records and provider_lanes -- authored through the
//	B/S-02.T3 portable migration builder (internal/storage/migrate),
//	exactly the ticket's task-3 wording ("single migration that creates
//	provider_records and provider_lanes tables").
//
// CONTRACT DEVIATION (domain registration, recorded, not papered over; see
// this ticket's journal for the full note). The contract text says
// "Register the 'providers' storage domain via B/S-03.T1 domain layout
// API." internal/storage/domains.go's AllDomains is CLOSED at eleven
// members (R-14.5, amended once by R-16.51 to add `policy`); there is no
// twelfth slot and this ticket has no standing to add one -- the same
// situation internal/fleet/sessions/domain.go and internal/retrieval/
// lifecycle/migrate.go each already hit and recorded identically. This
// package therefore does NOT call storage.Bootstrap or claim a DomainID.
// It authors its own migrate.MigrationSet against cascade.db's raw *sql.DB,
// exactly as internal/context/scope/schema.go does for the same reason.
//
// SCHEMA VERSION (R-14.198). applied_migrations keys schema_version
// GLOBALLY with no per-MigrationSet identity (internal/context/scope/
// schema.go's scopeSchemaVersion doc comment documents this in full).
// The claimed slots in the tree as of this ticket: bootstrap=1,
// context/scope=2, retrieval/lifecycle=3. This package claims the next
// unused slot, 4. The composition root's reader ceiling
// (cmd/cascade/daemon_unix_store.go's runtimeReaderCeiling) would need a
// term for registry.SchemaVersion, but that file is outside this ticket's
// files_scope (files_scope.change is empty) -- wiring it in is left to the
// composition-root ticket that constructs a live Registry, matching the
// unretired providers/anthropic.New-class testonly-allow.json entries this
// same wiring gap already documents for the driver constructors.
//
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2).

package registry

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names, prefixed per the ticket's exact wording -- no domain
// TablePrefix convention applies since this is not one of the eleven
// closed domains (see this file's CONTRACT DEVIATION note above).
const (
	tableProviderRecords = "provider_records"
	tableProviderLanes   = "provider_lanes"
)

// registrySchemaVersion is this package's MigrationSet target version --
// the next unused slot in the single global sequence. See this file's
// SCHEMA VERSION doc comment.
const registrySchemaVersion = 4

// SchemaVersion is registrySchemaVersion exported for a future composition
// root's reader-ceiling max(), matching scope.SchemaVersion's and
// lifecycle.SchemaVersion's own exported-for-the-same-reason pattern.
const SchemaVersion = registrySchemaVersion

// MigrationSet is the providers registry's two-table schema.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        registrySchemaVersion,
		MinimumReaderVersion: registrySchemaVersion,
		Steps: []migrate.MigrationStep{
			providerRecordsTableStep(),
			providerLanesTableStep(),
		},
	}
}

// providerRecordsTableStep is the provider_records create-table step: one
// row per registered provider.
func providerRecordsTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "provider_records: one row per registered provider",
		Table: &migrate.TableDef{
			Name: tableProviderRecords,
			Columns: []migrate.ColumnDef{
				{Name: "name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "driver_kind", Type: migrate.TypeText, NotNull: true},
				{Name: "base_url", Type: migrate.TypeText},
				{Name: "auth_type", Type: migrate.TypeText, NotNull: true},
				{Name: "auth_ref", Type: migrate.TypeText, NotNull: true},
				{Name: "known_models", Type: migrate.TypeText, NotNull: true},
				{Name: "account_kind", Type: migrate.TypeText, NotNull: true},
				{Name: "tier", Type: migrate.TypeText, NotNull: true},
				{Name: "capabilities", Type: migrate.TypeText, NotNull: true},
				{Name: "capabilities_probed_at", Type: migrate.TypeInteger},
				{Name: "cost", Type: migrate.TypeText},
				{Name: "health_status", Type: migrate.TypeText, NotNull: true},
				{Name: "health_checked_at", Type: migrate.TypeInteger},
				{Name: "demotion_count", Type: migrate.TypeInteger, NotNull: true},
				{Name: "created_at", Type: migrate.TypeInteger, NotNull: true},
				{Name: "updated_at", Type: migrate.TypeInteger, NotNull: true},
			},
		},
	}
}

// providerLanesTableStep is the provider_lanes create-table step: one row
// per named lane, FK'd to its owning provider record.
func providerLanesTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "provider_lanes: one row per named lane, FK'd to its owning provider record",
		Table: &migrate.TableDef{
			Name: tableProviderLanes,
			Columns: []migrate.ColumnDef{
				{Name: "lane_name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "provider_name", Type: migrate.TypeText, NotNull: true},
				{Name: "model_filter", Type: migrate.TypeText, NotNull: true},
				{Name: "weight", Type: migrate.TypeInteger, NotNull: true},
				{Name: "pool_membership", Type: migrate.TypeText, NotNull: true},
				{Name: "pool_index", Type: migrate.TypeInteger, NotNull: true},
				{Name: "capacity", Type: migrate.TypeText, NotNull: true},
				{Name: "state", Type: migrate.TypeText, NotNull: true},
				{Name: "reset_estimate", Type: migrate.TypeInteger},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "provider_name", RefTable: tableProviderRecords, RefColumn: "name", OnDelete: "CASCADE"},
			},
		},
	}
}

// ApplyMigrationSchema idempotently applies MigrationSet against db.
// dbPath/backupDir enable migrate's SQLite snapshot when non-empty; either
// may be left empty to disable it (an in-memory test database has nothing
// to snapshot).
func ApplyMigrationSchema(ctx context.Context, db *sql.DB, dialect migrate.Dialect, clock migrate.Clock, dbPath, backupDir string) error {
	if db == nil {
		return cascade.New(cascade.KindInvalidInput, "registry: ApplyMigrationSchema requires a non-nil db")
	}
	if clock == nil {
		return cascade.New(cascade.KindInvalidInput, "registry: ApplyMigrationSchema requires a non-nil Clock")
	}
	return migrate.Apply(ctx, migrate.ApplyConfig{
		DB:        db,
		Dialect:   dialect,
		Clock:     clock,
		DBPath:    dbPath,
		BackupDir: backupDir,
	}, MigrationSet())
}
