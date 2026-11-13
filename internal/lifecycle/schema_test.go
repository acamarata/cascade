package lifecycle

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSchemaMigrationsRun verifies that runMigrations creates all 11 tables
// without error and is idempotent on re-run.
func TestSchemaMigrationsRun(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Idempotent re-run.
	if err := runMigrations(db); err != nil {
		t.Fatalf("second run: %v", err)
	}

	tables := []string{
		"project_cfg",
		"phase_state",
		"transition",
		"decision",
		"authorization",
		"step",
		"owner_queue",
		"touchpoint",
		"wait",
		"hunt",
		"meta",
	}

	for _, tbl := range tables {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", tbl, err)
		}
	}

	// Verify UNIQUE constraint on owner_queue(phase, kind, subject).
	_, err = db.Exec(`INSERT INTO owner_queue (phase, kind, subject) VALUES ('p','k','s')`)
	if err != nil {
		t.Fatalf("insert owner_queue: %v", err)
	}
	_, err = db.Exec(`INSERT INTO owner_queue (phase, kind, subject) VALUES ('p','k','s')`)
	if err == nil {
		t.Fatal("expected unique constraint violation on owner_queue")
	}
}
