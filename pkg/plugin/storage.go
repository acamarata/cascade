package plugin

import (
	"context"
)

// Purpose: the public PluginStorage ABI — the namespaced Get/Set/List/
//   Delete/Migrate surface plugin authors code against (manifest v2 plugin
//   ABI). This file declares the interface and the portable migration DSL
//   only; the host binds a concrete internal/storage implementation at
//   load time (internal/storage/plugin.go, internal/storage/plugin_
//   migrate.go).
// Inputs: a context and a namespace-relative key/prefix on every call;
//   []MigrationStep values on Migrate.
// Outputs: values as []byte, a []string of keys, or a *cascade.Error.
// Constraints: pkg/plugin never imports internal/ (Art.10.2). This
//   interface is part of the plugin ABI and therefore an external contract
//   per Art.2's enumeration — see storage_test.go's spec-conformance test
//   and internal/storage/testdata's provenance record for what that
//   ticket does and does not claim.
// SPORT: pkg.plugin.Storage/ADDED (P1-E15-W4-S32-T3).

// Storage is the namespaced storage surface a plugin is bound to at
// load time. Every method is scoped to the plugin's own domain slot
// ("plugin.<manifest-id>"); reaching into another plugin's slot requires
// Get/Set/List/Delete on THIS plugin's own storage plus a granted
// plugin.cross_domain.<target> capability — this interface has no
// separate cross-domain method, because the host binds one PluginStorage
// value per plugin and the cross-domain gate lives in the concrete
// implementation the host constructs (internal/storage.PluginStorage),
// not in the ABI shape itself.
type Storage interface {
	// Get returns the value stored under key, or a cascade.KindNotFound
	// error if no such key exists in this plugin's domain.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set writes value under key, creating or overwriting the existing
	// entry. A value whose content matches the host's credential-shape
	// detector is refused with a typed sensitive-payload error rather than
	// being stored — see the concrete implementation's documentation for
	// the exact error type, which this interface cannot name without
	// importing internal/ (Art.10.2).
	Set(ctx context.Context, key string, value []byte) error

	// List returns every key in this plugin's domain with the given
	// prefix, in ascending key order. An empty prefix lists every key.
	List(ctx context.Context, prefix string) ([]string, error)

	// Delete removes key. Deleting an already-absent key is not an error.
	Delete(ctx context.Context, key string) error

	// Migrate applies migrations to this plugin's schema, in ascending
	// Version order, at install/upgrade time. A call whose highest Version
	// is already applied is a no-op: it performs no writes and returns a
	// MigrationReport reporting AlreadyCurrent. Migrations are forward-only
	// — see MigrationStep's doc comment — and a failed migration leaves the
	// store unchanged rather than half-applied.
	Migrate(ctx context.Context, migrations []Migration) (MigrationReport, error)
}

// ColumnType is the portable column type a plugin migration step declares.
// Mirrors internal/storage/migrate.ColumnType's closed set exactly
// (Text/Integer/Blob), redeclared here because pkg/plugin cannot import
// internal/ (Art.10.2) — the host's concrete Migrate implementation maps
// this 1:1 onto migrate.ColumnType.
type ColumnType int

const (
	// ColumnText is a portable text column.
	ColumnText ColumnType = iota
	// ColumnInteger is a portable integer column (64-bit on both dialects
	// for an autoincrement primary key — see the host implementation).
	ColumnInteger
	// ColumnBlob is a portable binary column.
	ColumnBlob
)

// ColumnDef declares one column of a plugin-owned table.
type ColumnDef struct {
	// Name is the column identifier.
	Name string
	// Type is the column's portable type.
	Type ColumnType
	// PrimaryKey marks this column as (part of) the table's primary key.
	PrimaryKey bool
	// AutoIncrement marks a single-column integer primary key as
	// auto-incrementing. Valid only when PrimaryKey is set and Type is
	// ColumnInteger.
	AutoIncrement bool
	// NotNull requires a non-NULL value.
	NotNull bool
}

// TableDef declares one table a plugin's migration creates.
//
// The name a plugin supplies here is NOT the literal table name the host
// creates: the host namespaces it under the plugin's own id
// ("plugin_<id>_<name>") so two plugins can never collide in cascade.db's
// shared schema, and so a plugin can never name (or overwrite) a table
// belonging to a cascade.db core domain.
type TableDef struct {
	// Name is the table's plugin-local identifier (unprefixed).
	Name string
	// Columns are the table's columns, in order. Must be non-empty.
	Columns []ColumnDef
}

// MigrationStep is one forward step of a plugin's schema. The DSL is
// deliberately CREATE-only, matching internal/storage/migrate's own
// forward-only design (dsl.go: "there is no ALTER TABLE step") — there is
// no down/rollback direction. A plugin that needs to reshape a table
// authors a new table under a new step rather than altering the old one.
type MigrationStep struct {
	// Table is this step's table definition. Required.
	Table TableDef
	// Description documents the step's intent for operators; not
	// interpreted by the host.
	Description string
}

// Migration is one version-numbered set of steps a plugin ships. Multiple
// Migration values passed to Migrate in one call are applied in
// ascending Version order.
type Migration struct {
	// Version is this migration's plugin-local schema version. Must be
	// >= 1 and strictly greater than any version already applied for this
	// plugin, except for the idempotent same-version re-apply case (see
	// MigrationReport.AlreadyCurrent).
	Version int
	// Steps are applied in order within this version.
	Steps []MigrationStep
	// PortabilityPostgresOnly routes this migration's DDL through the
	// Postgres dialect exclusively (portability: postgres-only manifest
	// flag) instead of the default local SQLite dialect.
	PortabilityPostgresOnly bool
}

// MigrationReport summarizes one Migrate call.
type MigrationReport struct {
	// AlreadyCurrent is true when the plugin's schema was already at the
	// highest Version passed in and no writes were performed.
	AlreadyCurrent bool
	// AppliedVersions lists the plugin-local versions actually applied by
	// this call, in ascending order. Empty when AlreadyCurrent is true.
	AppliedVersions []int
	// CurrentVersion is the plugin's schema version after this call.
	CurrentVersion int
}

// The stable message fragments the concrete host implementation's typed
// errors carry, exported so a plugin author can match on Error() content
// in a pinch without importing internal/ (Art.10.2 forbids a typed-error
// import from this package). Prefer cascade.KindOf(err) over string
// matching wherever possible.
const (
	// MsgSensitivePayload marks the sensitive-payload guard refusal.
	MsgSensitivePayload = "plugin storage: value matches a credential-shaped pattern, refusing to store"
	// MsgCrossDomainDenied marks the cross-domain capability gate refusal.
	MsgCrossDomainDenied = "plugin storage: cross-domain access denied (no matching capability grant)"
)
