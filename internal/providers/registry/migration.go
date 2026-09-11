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
// It authors its own migrate.MigrationSet.
//
// DATABASE CORRECTION (R-16.77). This package's schema does NOT target
// cascade.db: cmd/cascade/provider_health_cmd.go opens it against a
// dedicated providers.db file (providerRegistryDBFile), entirely separate
// from cascade.db's own applied_migrations ledger. An earlier version of
// this comment claimed a "claimed slot" (4) in cascade.db's (pre-R-16.77)
// global schema_version sequence, and a composition-root ceiling term
// this package supposedly still needed; both claims were wrong -- this
// package never touched cascade.db, so no cascade.db reader ceiling ever
// needed a term for it, and R-16.77 gave the ledger PER-SET identity
// (key: (SetID, schema_version)) so version numbers are no longer a
// tree-wide scarce resource for anyone regardless. This package's
// MigrationSet carries its own SetID ("providers-registry") and its own
// independent schema_version.
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
	tableProviderRecords    = "provider_records"
	tableProviderLanes      = "provider_lanes"
	tableProviderLaneProbes = "provider_lane_probes"
)

// registrySchemaVersion is this package's MigrationSet target version --
// the next unused slot in this SetID's own independent sequence (R-16.77
// gave the ledger per-(SetID, schema_version) identity, so this number is
// no longer shared with any other package). Bumped 4 -> 5 by
// P1-E12-W3-S25-T5 (R-16.78 §1) to add provider_lane_probes.
const registrySchemaVersion = 5

// SchemaVersion is registrySchemaVersion exported for a future composition
// root's reader-ceiling max(), matching scope.SchemaVersion's and
// lifecycle.SchemaVersion's own exported-for-the-same-reason pattern.
const SchemaVersion = registrySchemaVersion

// MigrationSet is the providers registry's three-table schema.
func MigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "providers-registry",
		SchemaVersion: registrySchemaVersion,
		ReaderCeiling: registrySchemaVersion,
		Steps: []migrate.MigrationStep{
			providerRecordsTableStep(),
			providerLanesTableStep(),
			providerLaneProbesTableStep(),
		},
	}
}

// providerLaneProbesTableStep is the provider_lane_probes create-table
// step (R-16.78 §1): one row per lane, holding ONLY that lane's latest
// probe/bench reading. This is the registry's OWN persisted shape --
// deliberately not internal/fleet.ProbeResult or BenchResult -- so the
// two packages never need to share a type and the dependency direction
// stays fleet -> registry, one way, never the reverse (see lanes.go's
// LaneProbeRecord doc comment for the full rationale). Latest-only by
// design: a new probe replaces the row rather than appending a history.
func providerLaneProbesTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "provider_lane_probes: one row per lane, its latest probe/bench reading only",
		Table: &migrate.TableDef{
			Name: tableProviderLaneProbes,
			Columns: []migrate.ColumnDef{
				{Name: "lane_name", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "latency_p50_ms", Type: migrate.TypeReal, NotNull: true},
				{Name: "latency_p95_ms", Type: migrate.TypeReal, NotNull: true},
				{Name: "error_rate", Type: migrate.TypeReal, NotNull: true},
				{Name: "cost_estimate", Type: migrate.TypeReal, NotNull: true},
				{Name: "probed_at", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "lane_name", RefTable: tableProviderLanes, RefColumn: "lane_name", OnDelete: "CASCADE"},
			},
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
