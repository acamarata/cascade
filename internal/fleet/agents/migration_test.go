package agents

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	_ "modernc.org/sqlite"
)

type testClock struct {
	t time.Time
}

func (c *testClock) Now() time.Time { return c.t }

func TestAgentMigrationIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	clk := &testClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}

	err = Migrate(ctx, db, migrate.SQLiteEmitter{}, clk)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='sessions_agents'").Scan(&name)
	if err != nil {
		t.Fatalf("table not found: %v", err)
	}

	err = Migrate(ctx, db, migrate.SQLiteEmitter{}, clk)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
