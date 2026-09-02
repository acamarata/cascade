package migrate

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: Apply is the package's single entry point — it bootstraps the
//
//	applied_migrations ledger table, refuses a downgrade
//	(*SchemaDowngradeError), snapshots the database before any new DDL
//	(§D-18, snapshot.go), and applies set's steps against db in dialect
//	SQL, recording one ledger row per newly-applied step. Re-running Apply
//	with the same set is a no-op (idempotent): every step's content
//	checksum is checked against the ledger before it is re-executed.
//
// Inputs: ApplyConfig (the target *sql.DB, its Dialect, an injected Clock,
//
//	and the optional DBPath/BackupDir pair that enables the §D-18
//	snapshot) plus the MigrationSet to apply.
//
// Outputs: nil on success (including the successful no-op case); a
//
//	*SchemaDowngradeError, *MigrationConflictError, or a *cascade.Error
//	from a failed DDL/snapshot/query on failure.
//
// Constraints: fail loud on checksum conflict — never silently overwrite
//
//	or re-execute a step whose recorded checksum differs from its current
//	content. The snapshot (if enabled) is written before the FIRST new
//	DDL statement executes and blocks the migration on failure
//	(fail-closed). See "Guarantees" below for exactly what is and is not
//	promised about crash-safety.
//
// Guarantees (read before relying on Apply for anything beyond this
// ticket's stated scope):
//   - Idempotent re-apply: calling Apply twice with an unchanged set
//     produces identical ledger rows both times and executes no DDL the
//     second time (TestIdempotentApply).
//   - Checksum-conflict detection: a step whose content changed after it
//     was recorded is caught BEFORE any further step in the same Apply
//     call executes (fail-fast within one call).
//   - Downgrade refusal: an on-disk schema_version newer than the
//     binary's MinimumReaderVersion is refused before any DDL runs.
//   - NOT guaranteed: atomicity of a single Apply call across MULTIPLE
//     steps. Each step is its own DDL statement plus its own ledger
//     INSERT; if the process dies between step N's DDL and step N's
//     ledger INSERT, step N's DDL has already taken effect (CREATE TABLE
//     IF NOT EXISTS / CREATE INDEX IF NOT EXISTS are themselves
//     idempotent, so re-running Apply is always safe), but the ledger row
//     for step N will be written on the NEXT successful Apply call, not
//     retroactively. A crash strictly between two steps therefore leaves
//     the schema ahead of what the ledger reports until Apply next runs
//     to completion — the schema itself is never left inconsistent
//     (every individual DDL statement is IF NOT EXISTS), only the
//     ledger's bookkeeping can lag by the steps not yet re-observed.
//   - NOT guaranteed: transactional rollback of a partially-applied
//     MigrationSet. There is no wrapping database transaction around the
//     whole Apply call (SQLite's DDL statements are not meaningfully
//     transactional across CREATE TABLE + CREATE INDEX in the way this
//     ticket's forward-only, IF-NOT-EXISTS design needs); safety instead
//     comes from every statement being independently idempotent plus the
//     §D-18 snapshot giving an operator a point-in-time rollback target.
//
// SPORT: internal.storage.migrate.Ledger/ADDED (P1-E02-W1-S02-T3).

// ledgerTableName is the applied-migrations ledger's fixed table name.
const ledgerTableName = "applied_migrations"

// ledgerDef is the DSL definition of the ledger table itself — the
// package dogfoods its own DSL to build this, so the ledger table gets
// the same identifier-safety and dialect-portability guarantees (and
// golden-fixture coverage) as any caller-authored table.
var ledgerDef = TableDef{
	Name: ledgerTableName,
	Columns: []ColumnDef{
		{Name: "id", Type: TypeInteger, PrimaryKey: true, AutoIncrement: true},
		{Name: "schema_version", Type: TypeInteger, NotNull: true},
		{Name: "checksum", Type: TypeText, NotNull: true},
		{Name: "applied_at", Type: TypeInteger, NotNull: true},
	},
}

// ApplyConfig configures Apply.
type ApplyConfig struct {
	// DB is the target connection. Every statement Apply executes runs
	// against this *sql.DB directly (no transaction wrapping — see
	// Apply's "Guarantees" doc comment).
	DB *sql.DB
	// Dialect selects the DDL emitter (SQLiteEmitter or PostgresEmitter).
	Dialect Dialect
	// Clock supplies the ledger's applied_at timestamps. Required.
	Clock Clock
	// DBPath is the on-disk path of the SQLite .db file backing DB, or ""
	// to disable the §D-18 snapshot entirely (Postgres, or an in-memory
	// SQLite test database with nothing to copy).
	DBPath string
	// BackupDir is the snapshot destination directory (typically
	// "<profile>/backups"). Required when DBPath is set.
	BackupDir string
}

// Apply is the package's entry point. See the package-level doc comment
// above for its full contract and guarantees.
func Apply(ctx context.Context, cfg ApplyConfig, set MigrationSet) error {
	if err := validateApplyConfig(cfg, set); err != nil {
		return err
	}

	if err := ensureLedgerTable(ctx, cfg.DB, cfg.Dialect); err != nil {
		return err
	}

	onDisk, err := currentSchemaVersion(ctx, cfg.DB)
	if err != nil {
		return err
	}
	if onDisk > set.MinimumReaderVersion {
		return newSchemaDowngradeError(onDisk, set.MinimumReaderVersion)
	}
	if set.SchemaVersion < onDisk {
		// Nothing to do: this set's target version has already been
		// superseded by a later one on disk. Forward-only, so this is a
		// successful no-op, not an error. (set.SchemaVersion == onDisk is
		// NOT short-circuited here — it still falls through to the
		// checksum-conflict check below, which is exactly the case that
		// catches a migration's content changing after it was already
		// applied.)
		return nil
	}

	applied, err := ledgerRowsForVersion(ctx, cfg.DB, set.SchemaVersion)
	if err != nil {
		return err
	}
	if err := verifyAppliedPrefix(applied, set); err != nil {
		return err
	}

	return applyRemainingSteps(ctx, cfg, set, len(applied), onDisk)
}

// validateApplyConfig checks the required ApplyConfig fields before Apply
// does any I/O.
func validateApplyConfig(cfg ApplyConfig, set MigrationSet) error {
	if cfg.DB == nil {
		return cascade.New(cascade.KindInvalidInput, "migrate: ApplyConfig.DB is required")
	}
	if cfg.Dialect == nil {
		return cascade.New(cascade.KindInvalidInput, "migrate: ApplyConfig.Dialect is required")
	}
	if cfg.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "migrate: ApplyConfig.Clock is required")
	}
	if cfg.DBPath != "" && cfg.BackupDir == "" {
		return cascade.New(cascade.KindInvalidInput, "migrate: ApplyConfig.BackupDir is required when DBPath is set")
	}
	if set.SchemaVersion < 1 {
		return cascade.Newf(cascade.KindInvalidInput, "migrate: MigrationSet.SchemaVersion must be >= 1, got %d", set.SchemaVersion)
	}
	return nil
}

// verifyAppliedPrefix compares the ledger's already-recorded rows for
// set.SchemaVersion (in application order) against the checksums of
// set.Steps at the same positions, returning *MigrationConflictError at
// the first mismatch. It does not compare positions beyond len(applied) —
// those are the new steps applyRemainingSteps will execute.
func verifyAppliedPrefix(applied []ledgerRow, set MigrationSet) error {
	for i, row := range applied {
		if i >= len(set.Steps) {
			// The ledger has more recorded steps for this version than
			// the current set defines — the set shrank after some of its
			// steps were already applied. Not this ticket's scope to
			// adjudicate further; the recorded history stands.
			break
		}
		want := stepChecksum(set.Steps[i])
		if row.checksum != want {
			return newMigrationConflictError(set.SchemaVersion, i, row.checksum, want)
		}
	}
	return nil
}

// applyRemainingSteps executes set.Steps[from:], snapshotting once before
// the first statement (if any) and inserting one ledger row per step.
// onDisk is the schema_version already committed to the ledger (computed
// once by Apply) — the version the §D-18 snapshot lets a caller roll back
// to.
func applyRemainingSteps(ctx context.Context, cfg ApplyConfig, set MigrationSet, from, onDisk int) error {
	if from >= len(set.Steps) {
		return nil // fully covered by the ledger already — idempotent no-op.
	}

	if _, err := snapshotBeforeMigrate(ctx, cfg.DB, cfg.DBPath, cfg.BackupDir, onDisk); err != nil {
		return err
	}

	for i := from; i < len(set.Steps); i++ {
		step := set.Steps[i]
		stmts, err := cfg.Dialect.Emit(MigrationSet{Steps: []MigrationStep{step}})
		if err != nil {
			return err
		}
		for _, stmt := range stmts {
			if _, err := cfg.DB.ExecContext(ctx, stmt); err != nil {
				return cascade.Wrapf(cascade.KindUnavailable, err, "migrate: apply schema_version %d step %d", set.SchemaVersion, i)
			}
		}
		if err := insertLedgerRow(ctx, cfg.DB, set.SchemaVersion, stepChecksum(step), cfg.Clock.Now()); err != nil {
			return err
		}
	}
	return nil
}
