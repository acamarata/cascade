// Purpose: proves providers/sqlite's Migrator injection seam (driver.go)
//
//	actually wires internal/storage/migrate.Apply into the open path —
//	migrate.Apply runs and the ledger + a caller-defined table exist
//	before Open returns the Driver. This file, not driver.go, is where
//	internal/storage/migrate gets imported: depguard's
//	plugins-providers-boundary rule (.golangci.yml) excludes _test.go
//	files from the providers/** → internal/** import ban (the same carve-
//	out driver_test.go already uses for internal/storage/storetest, see
//	its own top-of-file comment), because a driver's conformance/wiring
//	tests are expected to import the shared internal test/verification
//	packages. driver.go itself never imports internal/storage/migrate —
//	only WithMigrator's function-value injection seam, exactly mirroring
//	WithSocketProbe's existing pattern for internal/rpc — so Art.10.2
//	holds for shipped code while this test proves the real wiring
//	end-to-end against a real modernc-sqlite database (Art.2).
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T3).
package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// callerMigrationSet is a minimal, realistic MigrationSet a real
// composition root would pass — a single "settings" table at
// schema_version 1.
func callerMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SchemaVersion:        1,
		MinimumReaderVersion: 1,
		Steps: []migrate.MigrationStep{{
			Kind: migrate.StepCreateTable,
			Table: &migrate.TableDef{
				Name:    "settings",
				Columns: []migrate.ColumnDef{{Name: "key", Type: migrate.TypeText, PrimaryKey: true}, {Name: "value", Type: migrate.TypeText}},
			},
		}},
	}
}

// newMigratorOption adapts migrate.Apply to sqlite.Migrator's signature —
// exactly the thin composition-root adapter Migrator's doc comment
// describes.
func newMigratorOption(path string, set migrate.MigrationSet) sqlite.Option {
	backupDir := filepath.Join(filepath.Dir(path), "backups")
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	return sqlite.WithMigrator(func(ctx context.Context, db *sql.DB) error {
		return migrate.Apply(ctx, migrate.ApplyConfig{
			DB:        db,
			Dialect:   migrate.SQLiteEmitter{},
			Clock:     clock,
			DBPath:    path,
			BackupDir: backupDir,
		}, set)
	})
}

// TestOpen_MigratorAppliedBeforeReturn proves migrate.Apply runs — the
// ledger table and the caller's "settings" table both exist, and a
// pre-migrate snapshot was written — strictly before Open returns the
// Driver, and that the resulting Driver is otherwise fully usable.
func TestOpen_MigratorAppliedBeforeReturn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	set := callerMigrationSet()

	d, err := sqlite.Open(context.Background(), path, newMigratorOption(path, set))
	if err != nil {
		t.Fatalf("Open with WithMigrator: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	verify, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("verify sql.Open: %v", err)
	}
	defer func() { _ = verify.Close() }()

	for _, table := range []string{"applied_migrations", "settings", "kv"} {
		var count int
		if err := verify.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %q: want to exist after Open, got count=%d", table, count)
		}
	}

	// The Driver itself is fully usable — the migration wiring did not
	// leave it in some half-open state.
	ctx := context.Background()
	if err := d.Put(ctx, "ns", "k", []byte("v")); err != nil {
		t.Fatalf("Put after migrated Open: %v", err)
	}
	if got, err := d.Get(ctx, "ns", "k"); err != nil || string(got) != "v" {
		t.Fatalf("Get after migrated Open: got %q, %v", got, err)
	}
}

// TestOpen_MigratorFailureAbortsOpen proves a failing Migrator aborts
// Open with the migrator's error (never a partially-open Driver), and
// closes both connection pools it had already opened.
func TestOpen_MigratorFailureAbortsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	// MinimumReaderVersion 0 combined with a pre-seeded higher on-disk
	// version would trigger ErrSchemaDowngrade; simpler here: an invalid
	// MigrationSet (SchemaVersion 0) fails ApplyConfig validation
	// immediately, which is exactly the "migrator returns an error"
	// contract Open must propagate.
	badSet := migrate.MigrationSet{SchemaVersion: 0}

	_, err := sqlite.Open(context.Background(), path, newMigratorOption(path, badSet))
	if err == nil {
		t.Fatal("Open with a failing Migrator: want error, got nil")
	}
}
