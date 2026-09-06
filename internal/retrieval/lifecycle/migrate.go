// Purpose: `cascade recall index migrate` — surfaces and runs the
// retrieval index domain's own schema migrations through the B/S-02.T3
// portable migration builder (internal/storage/migrate).
//
// CONTRACT NOTE (both sides quoted in full in the journal). The retrieval
// domain's own on-disk layout has NO SQL schema to migrate: every S-10
// leg persists through pkg/provider.Store's generic key-value table
// (internal/retrieval/fts5_schema.go's own recorded CONTRACT DEVIATION —
// "the production migration set is deliberately empty ... adding
// speculative steps with nothing to migrate would be its own Article-1
// violation," the exact reasoning cmd/cascade/daemon_unix_store.go
// already applies to the runtime domain). This ticket's Migrate follows
// that same precedent: a real MigrationSet with zero Steps, applied for
// real through migrate.Apply, so the ledger, the schema_version stamp,
// the older-binary refusal and the pre-migration snapshot are all
// genuinely exercised — a real orchestration with nothing to converge
// today, not a stub with something faked.
//
// SCHEMA VERSION (R-14.198). applied_migrations keys schema_version
// GLOBALLY with no per-MigrationSet identity (internal/context/scope/
// schema.go's scopeSchemaVersion doc comment documents this in full).
// bootstrap claims 1, the context/scope domain claims 2; this package
// claims the next unused slot, 3. The composition root's reader ceiling
// (cmd/cascade/daemon_unix_store.go's runtimeReaderCeiling) MUST add a
// term for SchemaVersion or the daemon refuses to reopen a database this
// migrate verb just wrote — see this ticket's journal for that wiring.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
package lifecycle

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// retrievalSchemaVersion is this package's MigrationSet target version —
// the next unused slot in the single global sequence (bootstrap=1,
// context/scope=2). See this file's SCHEMA VERSION doc comment.
const retrievalSchemaVersion = 3

// SchemaVersion is retrievalSchemaVersion exported for the composition
// root's reader-ceiling max(), matching scope.SchemaVersion's own
// exported-for-the-same-reason pattern exactly.
const SchemaVersion = retrievalSchemaVersion

// RetrievalMigrationSet is the retrieval index domain's MigrationSet. Its
// Steps are empty today (this file's CONTRACT NOTE); MinimumReaderVersion
// equals SchemaVersion so no binary older than the one that introduced
// this call can reopen a database this verb has migrated.
func RetrievalMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{SchemaVersion: retrievalSchemaVersion, MinimumReaderVersion: retrievalSchemaVersion}
}

// MigrateDeps configures Migrate. It is deliberately independent of
// Manager: migrate operates on cascade.db's raw *sql.DB, which nothing
// else in this package needs, mirroring internal/daemon/context_scope.go's
// choice to open its own second connection rather than widen every other
// signature in the composition root.
type MigrateDeps struct {
	DB        *sql.DB
	Dialect   migrate.Dialect
	Clock     runtime.Clock
	DBPath    string
	BackupDir string
}

// MigrateResult reports one migrate run's outcome.
type MigrateResult struct {
	// Applied is true when this call advanced the ledger. False on an
	// idempotent second run: "nothing to do" per the ticket's acceptance
	// criterion.
	Applied bool
	// FromVersion is the schema_version on disk before this call.
	FromVersion int
	// ToVersion is RetrievalMigrationSet's SchemaVersion.
	ToVersion int
}

// Migrate runs RetrievalMigrationSet through migrate.Apply.
func Migrate(ctx context.Context, deps MigrateDeps) (MigrateResult, error) {
	if err := validateMigrateDeps(deps); err != nil {
		return MigrateResult{}, err
	}
	before, err := currentLedgerVersion(ctx, deps.DB)
	if err != nil {
		return MigrateResult{}, err
	}
	set := RetrievalMigrationSet()
	if err := migrate.Apply(ctx, migrate.ApplyConfig{
		DB: deps.DB, Dialect: deps.Dialect, Clock: deps.Clock, DBPath: deps.DBPath, BackupDir: deps.BackupDir,
	}, set); err != nil {
		return MigrateResult{}, err
	}
	// Applied is meaningful only when the set actually carries steps:
	// migrate.Apply's applyRemainingSteps never inserts a ledger row for
	// an empty Steps slice (there is nothing to record a checksum for),
	// so an empty set — RetrievalMigrationSet's shape today, per this
	// file's CONTRACT NOTE — has nothing to converge on ANY run, first
	// included, and Applied is honestly always false rather than
	// fabricating a "converged" signal the ledger never recorded.
	applied := len(set.Steps) > 0 && before < set.SchemaVersion
	return MigrateResult{Applied: applied, FromVersion: before, ToVersion: set.SchemaVersion}, nil
}

// validateMigrateDeps checks the required fields before any I/O.
func validateMigrateDeps(deps MigrateDeps) error {
	switch {
	case deps.DB == nil:
		return cascade.New(cascade.KindInvalidInput, "lifecycle: migrate: no database connection")
	case deps.Dialect == nil:
		return cascade.New(cascade.KindInvalidInput, "lifecycle: migrate: no dialect")
	case deps.Clock == nil:
		return cascade.New(cascade.KindInvalidInput, "lifecycle: migrate: no clock")
	}
	return nil
}

// currentLedgerVersion reads applied_migrations' highest recorded
// schema_version directly. "applied_migrations" is the ledger's
// documented, reserved table name (internal/storage/migrate/ledger.go:
// "reservedLedgerName (R-14.143)"); the column reader itself
// (currentSchemaVersion) is unexported and internal/storage/migrate is
// outside this ticket's files_scope, so this file queries the same public
// table by its own published name rather than adding an export to
// another package's file. A table that does not exist yet (first-ever
// migrate on a fresh database, before Apply's ensureLedgerTable runs)
// reads as version 0, matching Apply's own pre-bootstrap behavior.
func currentLedgerVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations`).Scan(&version)
	if err != nil {
		// Table absent: report 0 rather than failing outright, since
		// migrate.Apply's own ensureLedgerTable call handles that case as
		// "nothing applied yet" too.
		return 0, nil
	}
	return int(version.Int64), nil
}
