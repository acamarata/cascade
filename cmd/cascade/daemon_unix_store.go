//go:build !windows

// Purpose: openRuntimeStore's real composition-root wiring — the sqlite
//
//	Migrator adapter (internal/storage/migrate.Apply, per
//	providers/sqlite/README.md "Migration boot path") and the domain
//	anchor-table bootstrap (internal/storage.Bootstrap), both run inside
//	the SAME sqlite.WithMigrator callback so both close over the one raw
//	*sql.DB the driver opens for writes. Split out of daemon_unix_run.go
//	under R-14.117 (Art.10.3's 300-line cap; daemon_unix_run.go was
//	already close to it, and this is its own composition-root concern —
//	closing R-14.175's migrator/Bootstrap gap pair — not a mechanical
//	relocation of unrelated code).
//
// Inputs: the same PathProvider and Clock openRuntimeStore's callers
//
//	already thread through daemonDeps.
//
// Outputs: a real provider.Store, the raw *sql.DB the Migrator callback
//
//	captured (needed by the scheduler's retention runnables, which run
//	direct SQL VACUUM/DELETE — pkg/provider.Store's KV contract has no
//	seam for either), and the store's closer.
//
// Constraints: no bare time.Now (Art.7.3 — Clock is threaded through to
//
//	both migrate.Apply and storage.Bootstrap); providers/sqlite itself
//	never imports internal/** (Art.10.2) — only this file, cmd/'s
//	composition root, imports both providers/sqlite and
//	internal/storage(/migrate).
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 migrator/Bootstrap wiring).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// runtimeSchemaVersion is the schema_version this composition root asks
// migrate.Apply to converge cascade.db to. There are no versioned steps
// beyond the base kv schema openLocked already creates and the ten
// R-14.5 domain anchor tables storage.Bootstrap stamps below, so
// runtimeMigrationSet's Steps is empty today — Apply still runs for
// real, still creates and owns the applied_migrations ledger, and is
// the forward seam every future schema change appends a MigrationStep
// to (R-14.175's gap was the missing CALL, not missing content; adding
// speculative steps with nothing to migrate would be its own Article-1
// violation).
const runtimeSchemaVersion = 1

// runtimeMigrationSet is the production MigrationSet openRuntimeStore's
// Migrator adapter applies. See runtimeSchemaVersion's doc comment for
// why Steps is empty as of this wiring.
func runtimeMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        runtimeSchemaVersion,
		MinimumReaderVersion: runtimeSchemaVersion,
	}
}

// newRuntimeMigrator builds the sqlite.Migrator this composition root
// installs: migrate.Apply (the versioned-ledger seam) followed by
// storage.Bootstrap (the ten R-14.5 domain anchor tables). Order is
// deliberate but not load-bearing — migrate.Apply's empty Steps set
// inserts no ledger row (nothing to check a checksum against), so
// storage.Bootstrap's own direct ledger stamp at schema_version 1 never
// collides with it (verified against internal/storage/migrate/ledger.go's
// verifyAppliedPrefix, which is vacuous whenever the caller's set has zero
// Steps). dbPath/backupDir enable migrate.Apply's §D-18 pre-migration
// snapshot; rawDB, once the callback runs, is the same *sql.DB the
// Driver's WriteExecutor uses for every write for the rest of the
// process's life — captured here because pkg/provider.Store's KV
// contract has no seam for the scheduler's retention runnables, which
// need direct SQL access (DELETE/VACUUM).
func newRuntimeMigrator(dbPath string, clock runtime.Clock, rawDB **sql.DB) sqlite.Migrator {
	return newRuntimeMigratorWithSet(dbPath, clock, rawDB, runtimeMigrationSet())
}

// newRuntimeMigratorWithSet is newRuntimeMigrator with the migration set
// injected. The production set has no steps yet, so migrate.Apply currently
// converges nothing and removing it from the callback changes no observable
// state. That makes the wiring untestable through the production set alone,
// which is why a test drives this same callback with a set that does carry a
// step: it proves the migrator is actually invoked, not merely constructed.
func newRuntimeMigratorWithSet(dbPath string, clock runtime.Clock, rawDB **sql.DB, set migrate.MigrationSet) sqlite.Migrator {
	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	return func(ctx context.Context, db *sql.DB) error {
		*rawDB = db
		if err := migrate.Apply(ctx, migrate.ApplyConfig{
			DB:        db,
			Dialect:   migrate.SQLiteEmitter{},
			Clock:     clock,
			DBPath:    dbPath,
			BackupDir: backupDir,
		}, set); err != nil {
			return err
		}
		if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
			return err
		}
		return nil
	}
}

// openRuntimeStore opens the real on-disk cascade.db (providers/sqlite's
// modernc-sqlite driver) under paths.DataDir(), wiring the real Migrator
// (migrate.Apply + storage.Bootstrap, see newRuntimeMigrator) so a
// shipped daemon actually converges its schema instead of shipping only
// the base kv table — the gap R-14.175 named. Returns the raw *sql.DB the
// Migrator callback captured alongside the provider.Store, since the
// scheduler's retention runnables (daemon_unix_scheduler.go) need it.
func openRuntimeStore(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (provider.Store, *sql.DB, func(), error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "create data directory")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	var rawDB *sql.DB
	driver, err := sqlite.Open(ctx, dbPath, sqlite.WithMigrator(newRuntimeMigrator(dbPath, clock, &rawDB)))
	if err != nil {
		return nil, nil, nil, err
	}
	return driver, rawDB, func() { _ = driver.Close() }, nil
}
