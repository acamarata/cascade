//go:build !windows

// Purpose: the direct invariant proof runtimeReaderCeiling exists for
//
//	(its own doc comment): a cascade.db already advanced to the HIGHEST
//	schema_version any bundled domain targets must still open through
//	openRuntimeStore, the real production path — not merely construct a
//	MigrationSet without error. The ceiling mechanism is incidental; this
//	test pins the outcome the ceiling exists to guarantee, so a future
//	change to how the ceiling is computed still has to keep this green.
//
// SPORT: cmd/cascade/daemon (ADD — schema ceiling coverage invariant).
package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// highestBundledSchemaVersion mirrors runtimeReaderCeiling's own max():
// the highest SchemaVersion any domain package this binary currently
// applies against cascade.db targets. Kept here as a plain value rather
// than calling the unexported function under test, so this test still
// pins the real invariant if runtimeReaderCeiling's implementation
// changes shape.
func highestBundledSchemaVersion() int {
	v := scope.SchemaVersion
	if lifecycle.SchemaVersion > v {
		v = lifecycle.SchemaVersion
	}
	return v
}

// advanceToHighestBundledSchema opens its own connection to dbPath (the
// documented tradeoff internal/daemon/context_scope.go and recall_index.go
// already use in production) and applies every domain schema
// openRuntimeStore's real sibling registrations apply, converging
// cascade.db's ledger to highestBundledSchemaVersion().
func advanceToHighestBundledSchema(t *testing.T, dbPath string, clock migrate.Clock) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("advanceToHighestBundledSchema: open: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupDir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		t.Fatalf("advanceToHighestBundledSchema: ApplyScopeSchema: %v", err)
	}
	_, err = lifecycle.Migrate(context.Background(), lifecycle.MigrateDeps{
		DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock, DBPath: dbPath, BackupDir: backupDir,
	})
	if err != nil {
		t.Fatalf("advanceToHighestBundledSchema: lifecycle.Migrate: %v", err)
	}
}

// TestOpenRuntimeStore_ReopensAtHighestBundledSchemaVersion is the
// invariant this phase's ceiling drift bug (R-14.198) broke in practice:
// seed cascade.db up to the highest schema_version any bundled domain
// targets, then reopen it through openRuntimeStore, the exact production
// entry point platformDaemonRun calls on every daemon start. A ceiling
// missing a term for that highest version makes this fail with a
// MigrationConflict-shaped error refusing to reopen the daemon's own
// database.
func TestOpenRuntimeStore_ReopensAtHighestBundledSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))

	_, _, closeFirst, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore (seed): %v", err)
	}
	closeFirst()

	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	advanceToHighestBundledSchema(t, dbPath, clock)

	_, _, closeReopen, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore must reopen a database at its own highest bundled schema_version (%d), got: %v",
			highestBundledSchemaVersion(), err)
	}
	closeReopen()
}
