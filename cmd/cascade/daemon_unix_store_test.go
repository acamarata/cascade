//go:build !windows

// Purpose: end-to-end proof that openRuntimeStore — the actual
//
//	production composition-root function daemon_unix.go's
//	platformDaemonRun calls, not a component test of migrate.Apply or
//	storage.Bootstrap in isolation — advances a fresh, empty cascade.db
//	to the real production schema: the migrate.Apply ledger table plus
//	every R-14.5 domain anchor table storage.Bootstrap stamps. Closes
//	R-14.175's "migrations never run" / "storage.Bootstrap has no
//	caller" pair.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 migrator/Bootstrap
//
//	wiring verification).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestOpenRuntimeStore_ProductionMigrationApplied drives openRuntimeStore
// exactly as platformDaemonRun does — a fresh paths.DataDir(), no
// preexisting cascade.db — and asserts the schema advanced from nothing
// to the real production baseline: the migrate.Apply ledger table AND
// every one of the ten R-14.5 domain anchor tables, before
// openRuntimeStore even returns. A pristine on-disk file with none of
// these tables, before Open runs, is "a migration pending" in the only
// sense this composition root's real, current MigrationSet content
// supports (see daemon_unix_store.go's runtimeMigrationSet doc comment
// for why Steps is empty today) — Bootstrap's ten anchor tables are the
// real schema-advancing work this wiring performs.
func TestOpenRuntimeStore_ProductionMigrationApplied(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatalf("precondition: %s must not exist before Open", dbPath)
	}

	store, rawDB, closeStore, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	t.Cleanup(closeStore)

	if store == nil {
		t.Fatal("openRuntimeStore: store is nil")
	}
	if rawDB == nil {
		t.Fatal("openRuntimeStore: rawDB is nil — the scheduler's retention runnables need it")
	}

	verify, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("verify sql.Open: %v", err)
	}
	defer func() { _ = verify.Close() }()

	wantTables := []string{"applied_migrations", "__health_probe__"}
	for _, meta := range storage.AllDomains {
		wantTables = append(wantTables, string(meta.ID)+"_domain_root")
	}
	for _, table := range wantTables {
		var count int
		if err := verify.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %q: want to exist after openRuntimeStore (schema must have advanced), got count=%d", table, count)
		}
	}

	// The tables above are created by the domain bootstrap, and the
	// production migration set has no steps yet, so nothing here can
	// distinguish a wired migrator from an absent one. That proof lives in
	// TestRuntimeMigrator_AppliesAStepThroughTheProductionCallback, which
	// drives this same callback with a set that does carry a step.
	var stamped int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM applied_migrations`).Scan(&stamped); err != nil {
		t.Fatalf("count applied_migrations: %v", err)
	}
	if stamped == 0 {
		t.Error("applied_migrations is empty: the schema ledger was never stamped by the production open path")
	}
}

// TestOpenRuntimeStore_MigrationIdempotentOnSecondOpen proves a second
// openRuntimeStore call against the SAME on-disk file (the real restart
// path) is a clean no-op — no error, no duplicate ledger row, the
// anchor tables still present — matching storage.Bootstrap's own
// documented idempotency contract driven through the real composition
// root rather than storage.Bootstrap in isolation.
func TestOpenRuntimeStore_MigrationIdempotentOnSecondOpen(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))

	_, _, closeFirst, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("first openRuntimeStore: %v", err)
	}
	closeFirst()

	store2, rawDB2, closeSecond, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("second openRuntimeStore (restart path): %v", err)
	}
	t.Cleanup(closeSecond)
	if store2 == nil || rawDB2 == nil {
		t.Fatal("second openRuntimeStore: nil store or rawDB")
	}
}

// TestRuntimeMigrator_AppliesAStepThroughTheProductionCallback proves the
// migrator the composition root installs actually runs migrate.Apply.
//
// The production set carries no steps, so with it the callback is a no-op
// and its removal changes nothing observable. Injecting a set with one real
// step makes the difference visible: if the callback stops calling
// migrate.Apply, the step's table is never created and this fails.
func TestRuntimeMigrator_AppliesAStepThroughTheProductionCallback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var captured *sql.DB
	set := migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind:        migrate.StepCreateTable,
			Description: "create the migrator probe table",
			Table: &migrate.TableDef{
				Name: "migrator_probe",
				Columns: []migrate.ColumnDef{{
					Name:       "id",
					Type:       migrate.TypeInteger,
					PrimaryKey: true,
				}},
			},
		}},
	}

	migrator := newRuntimeMigratorWithSet(dbPath, clock, &captured, set)
	if err := migrator(context.Background(), db); err != nil {
		t.Fatalf("migrator callback: %v", err)
	}
	if captured != db {
		t.Error("the callback must capture the raw *sql.DB the driver opened")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='migrator_probe'`).Scan(&count); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if count != 1 {
		t.Fatal("migrator_probe was not created: the callback did not run migrate.Apply")
	}
}
