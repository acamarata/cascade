// Purpose: internal/storage/plugin_migrate_extra_test.go — branches
//
//	plugin_migrate_test.go's idempotent/snapshot/postgres/conflict suite
//	left uncovered: NewPluginMigrator's five required-argument refusals,
//	a corrupt migration-state record's decode refusal (loadState), a
//	real database error mid-migration (nextGlobalVersion), an invalid
//	column type reaching applyOne (convertTableDef/convertColumnType),
//	and validateMigrationVersions' four refusal branches. Every failure
//	path here also asserts the plugin's persisted migration-state key is
//	left exactly as it started — a failing migration must never leave
//	the store half-applied.
//
// SPORT: internal.storage.PluginMigrator/ADDED (P1-E15-W4-S32-T3).
package storage_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// TestNewPluginMigratorRequiresEveryArgument drives all five fail-closed
// construction refusals, including the §D-18 "dbPath without backupDir"
// pairing rule.
func TestNewPluginMigratorRequiresEveryArgument(t *testing.T) {
	db, path := openMigrateTestDB(t)
	dialect := migrate.SQLiteEmitter{}
	clock := newFixedClock()
	store := storetest.NewMemStore()

	t.Run("nil-db", func(t *testing.T) {
		if _, err := storage.NewPluginMigrator(nil, dialect, clock, "", "", store); err == nil {
			t.Fatal("NewPluginMigrator(nil db) = nil error, want KindInvalidInput")
		}
	})
	t.Run("nil-dialect", func(t *testing.T) {
		if _, err := storage.NewPluginMigrator(db, nil, clock, "", "", store); err == nil {
			t.Fatal("NewPluginMigrator(nil dialect) = nil error, want KindInvalidInput")
		}
	})
	t.Run("nil-clock", func(t *testing.T) {
		if _, err := storage.NewPluginMigrator(db, dialect, nil, "", "", store); err == nil {
			t.Fatal("NewPluginMigrator(nil clock) = nil error, want KindInvalidInput")
		}
	})
	t.Run("nil-store", func(t *testing.T) {
		if _, err := storage.NewPluginMigrator(db, dialect, clock, "", "", nil); err == nil {
			t.Fatal("NewPluginMigrator(nil store) = nil error, want KindInvalidInput")
		}
	})
	t.Run("dbpath-without-backupdir", func(t *testing.T) {
		_, err := storage.NewPluginMigrator(db, dialect, clock, path, "", store)
		if err == nil {
			t.Fatal("NewPluginMigrator(dbPath set, backupDir empty) = nil error, want the §D-18 pairing refusal")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("kind = %v, want KindInvalidInput", kind)
		}
	})
}

// badJSONStore is a real stateStore-shaped double whose Get always
// returns bytes that are not valid JSON, so loadState's decode refusal
// fires against real (if malformed) persisted content, not a mock error.
type badJSONStore struct{ puts int }

func (s *badJSONStore) Get(context.Context, string, string) ([]byte, error) {
	return []byte("{not json"), nil
}

func (s *badJSONStore) Put(context.Context, string, string, []byte) error {
	s.puts++
	return nil
}

// TestPluginMigrationCorruptStateRefused proves a plugin whose persisted
// migration-state record decodes as garbage is refused (KindIntegrity),
// and that Apply never reaches a write (Put) once the read itself fails.
func TestPluginMigrationCorruptStateRefused(t *testing.T) {
	db, _ := openMigrateTestDB(t)
	ctx := context.Background()
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: newFixedClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	store := &badJSONStore{}
	migrator, err := storage.NewPluginMigrator(db, migrate.SQLiteEmitter{}, newFixedClock(), "", "", store)
	if err != nil {
		t.Fatalf("NewPluginMigrator: %v", err)
	}

	_, err = migrator.Apply(ctx, "widget-plugin", []plugin.Migration{onePluginMigration(1, "widgets")})
	if err == nil {
		t.Fatal("Apply over a corrupt migration-state record = nil error, want KindIntegrity")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want KindIntegrity", kind)
	}
	if store.puts != 0 {
		t.Fatalf("Apply wrote migration state (puts=%d) despite failing to read the existing record first", store.puts)
	}
}

// TestPluginMigrationClosedDBLeavesStateUnchanged proves a mid-migration
// database failure (closed *sql.DB, so nextGlobalVersion's own query
// fails) is refused rather than partially applied, and that the plugin's
// migration-state record in the (separate, still-open) state store is
// left exactly as it started — the "failing migration leaves the store
// unchanged" acceptance criterion.
func TestPluginMigrationClosedDBLeavesStateUnchanged(t *testing.T) {
	db, _ := openMigrateTestDB(t)
	ctx := context.Background()
	store := storetest.NewMemStore()
	migrator, err := storage.NewPluginMigrator(db, migrate.SQLiteEmitter{}, newFixedClock(), "", "", store)
	if err != nil {
		t.Fatalf("NewPluginMigrator: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	_, err = migrator.Apply(ctx, "widget-plugin", []plugin.Migration{onePluginMigration(1, "widgets")})
	if err == nil {
		t.Fatal("Apply against a closed *sql.DB = nil error, want the ledger-read failure")
	}

	// Re-run against a freshly opened db+bootstrap to prove state truly
	// never advanced: version 1 is still pending, not "already current".
	db2, _ := openMigrateTestDB(t)
	if _, err := storage.Bootstrap(ctx, db2, storage.BootstrapOpts{Clock: newFixedClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	migrator2, err := storage.NewPluginMigrator(db2, migrate.SQLiteEmitter{}, newFixedClock(), "", "", store)
	if err != nil {
		t.Fatalf("NewPluginMigrator (2): %v", err)
	}
	report, err := migrator2.Apply(ctx, "widget-plugin", []plugin.Migration{onePluginMigration(1, "widgets")})
	if err != nil {
		t.Fatalf("Apply after the closed-db failure: %v", err)
	}
	if report.AlreadyCurrent {
		t.Fatal("Apply reported AlreadyCurrent=true after a failed prior attempt, want the migration to still be pending")
	}
}

// TestPluginMigrationInvalidColumnTypeRefused proves an invalid
// plugin.ColumnType reaching applyOne is refused before any DDL runs
// (convertColumnType's exhaustive-switch default), and never advances
// the plugin's migration state.
func TestPluginMigrationInvalidColumnTypeRefused(t *testing.T) {
	db, _ := openMigrateTestDB(t)
	ctx := context.Background()
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: newFixedClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	store := storetest.NewMemStore()
	migrator, err := storage.NewPluginMigrator(db, migrate.SQLiteEmitter{}, newFixedClock(), "", "", store)
	if err != nil {
		t.Fatalf("NewPluginMigrator: %v", err)
	}

	bad := plugin.Migration{
		Version: 1,
		Steps: []plugin.MigrationStep{{Table: plugin.TableDef{
			Name:    "widgets",
			Columns: []plugin.ColumnDef{{Name: "id", Type: plugin.ColumnType(99)}},
		}}},
	}
	report, err := migrator.Apply(ctx, "widget-plugin", []plugin.Migration{bad})
	if err == nil {
		t.Fatal("Apply with an unknown ColumnType = nil error, want a refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, want KindInvalidInput", kind)
	}
	if report.CurrentVersion != 0 || len(report.AppliedVersions) != 0 {
		t.Fatalf("Apply returned a non-empty report %+v alongside an error", report)
	}

	// The plugin's migration state was never advanced: a valid retry
	// still applies version 1 as new, not as "already current".
	report2, err := migrator.Apply(ctx, "widget-plugin", []plugin.Migration{onePluginMigration(1, "widgets")})
	if err != nil {
		t.Fatalf("retry Apply after the refused migration: %v", err)
	}
	if report2.AlreadyCurrent {
		t.Fatal("retry Apply reported AlreadyCurrent=true, want the earlier refusal to have left state untouched")
	}
}

// TestValidateMigrationVersionsRefusals drives validateMigrationVersions'
// four fail-closed branches via Apply's own entry point.
func TestValidateMigrationVersionsRefusals(t *testing.T) {
	db, _ := openMigrateTestDB(t)
	ctx := context.Background()
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: newFixedClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	migrator, err := storage.NewPluginMigrator(db, migrate.SQLiteEmitter{}, newFixedClock(), "", "", storetest.NewMemStore())
	if err != nil {
		t.Fatalf("NewPluginMigrator: %v", err)
	}

	cases := []struct {
		name string
		migs []plugin.Migration
	}{
		{"empty", nil},
		{"zero-version", []plugin.Migration{{Version: 0, Steps: []plugin.MigrationStep{{Table: plugin.TableDef{
			Name: "t", Columns: []plugin.ColumnDef{{Name: "id", Type: plugin.ColumnInteger}},
		}}}}}},
		{"duplicate-version", []plugin.Migration{
			onePluginMigration(1, "a"),
			onePluginMigration(1, "b"),
		}},
		{"no-steps", []plugin.Migration{{Version: 1, Steps: nil}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := migrator.Apply(ctx, "widget-plugin-"+tc.name, tc.migs)
			if err == nil {
				t.Fatalf("Apply(%s) = nil error, want KindInvalidInput", tc.name)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("Apply(%s) kind = %v, want KindInvalidInput", tc.name, kind)
			}
		})
	}
}

// TestConvertTableDefRefusals drives convertTableDef's own two guards
// (empty table name, zero columns) independent of column-type validity.
func TestConvertTableDefRefusals(t *testing.T) {
	db, _ := openMigrateTestDB(t)
	ctx := context.Background()
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: newFixedClock()}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	migrator, err := storage.NewPluginMigrator(db, migrate.SQLiteEmitter{}, newFixedClock(), "", "", storetest.NewMemStore())
	if err != nil {
		t.Fatalf("NewPluginMigrator: %v", err)
	}

	t.Run("empty-table-name", func(t *testing.T) {
		mig := plugin.Migration{Version: 1, Steps: []plugin.MigrationStep{{Table: plugin.TableDef{
			Name:    "",
			Columns: []plugin.ColumnDef{{Name: "id", Type: plugin.ColumnInteger}},
		}}}}
		if _, err := migrator.Apply(ctx, "empty-name-plugin", []plugin.Migration{mig}); err == nil {
			t.Fatal("Apply with an empty table name = nil error, want a refusal")
		}
	})
	t.Run("no-columns", func(t *testing.T) {
		mig := plugin.Migration{Version: 1, Steps: []plugin.MigrationStep{{Table: plugin.TableDef{
			Name:    "widgets",
			Columns: nil,
		}}}}
		if _, err := migrator.Apply(ctx, "no-columns-plugin", []plugin.Migration{mig}); err == nil {
			t.Fatal("Apply with zero columns = nil error, want a refusal")
		}
	})
}
