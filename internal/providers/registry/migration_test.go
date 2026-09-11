package registry

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock/Clock for tests -- no bare time.Now
// (Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a fresh in-memory modernc SQLite database for one test.
// Each test gets its own named in-memory database (a unique DSN) so
// parallel tests never share state.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestRegistry opens an in-memory DB, applies the migration, and
// returns a ready-to-use Registry.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return NewRegistry(db, newTestClock())
}

func TestMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("first ApplyMigrationSchema on empty DB: %v", err)
	}
	for _, table := range []string{tableProviderRecords, tableProviderLanes} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}

	// Re-apply on the already-migrated DB: must be a clean no-op, not an
	// error and not a duplicate-table failure.
	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema on pre-migrated DB: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(nil db, ...) should have failed")
	}
	db := openTestDB(t)
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(..., nil clock, ...) should have failed")
	}
}

func TestMigrationMinimumReaderVersionRefusesDowngrade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}

	// A set claiming a lower MinimumReaderVersion than the ledger's
	// on-disk schema_version must be refused, exercising the
	// minimum_reader_version check the acceptance criteria names.
	downgraded := MigrationSet()
	downgraded.MinimumReaderVersion = registrySchemaVersion - 1
	if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: dialect, Clock: clock}, downgraded); err == nil {
		t.Fatal("Apply with a lowered MinimumReaderVersion should have been refused, got nil error")
	}
}
