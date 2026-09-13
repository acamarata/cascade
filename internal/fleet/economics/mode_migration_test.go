package economics

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// testClock is a minimal economics.Clock for tests that do not need a
// mutable frozen clock.
type testClock struct{ t time.Time }

func (c testClock) Now() time.Time { return c.t }

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/economics.db?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := testClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return db
}

func TestApplyMigrationSchemaIdempotent(t *testing.T) {
	db := newTestDB(t)
	clock := testClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDBAndClock(t *testing.T) {
	if err := ApplyMigrationSchema(context.Background(), nil, migrate.SQLiteEmitter{}, testClock{}, "", ""); err == nil {
		t.Error("want error for nil db")
	}
	db := newTestDB(t)
	if err := ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Error("want error for nil clock")
	}
}
