package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/storage/migrate"
)

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock {
	return fakeClock{t: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
}

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

func newTestManager(t *testing.T, clk Clock) *Manager {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return NewManager(db, clk)
}

// TestMigrationIdempotent: applying the schema twice (empty DB, then a
// pre-migrated DB) both exit cleanly, and minimum_reader_version is
// exercised.
func TestMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("first ApplyMigrationSchema on empty DB: %v", err)
	}
	var name string
	if err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableProviderUsage).Scan(&name); err != nil {
		t.Fatalf("provider_usage table missing after first apply: %v", err)
	}
	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema on a pre-migrated DB: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDB(t *testing.T) {
	if err := ApplyMigrationSchema(context.Background(), nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("expected an error for a nil db")
	}
}

func TestApplyMigrationSchemaRequiresClock(t *testing.T) {
	db := openTestDB(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("expected an error for a nil clock")
	}
}
