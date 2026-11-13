package docket

import (
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})
	return db
}

func TestLedgerMigrationCreatesTables(t *testing.T) {
	db := newTestDB(t)
	if err := ApplyLedgerMigration(db); err != nil {
		t.Fatalf("ApplyLedgerMigration: %v", err)
	}

	tables := []string{"ticket_ledger", "owner_item", "assumption"}
	for _, tbl := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", tbl, err)
		}
	}
}

func TestLedgerMigrationColumns(t *testing.T) {
	db := newTestDB(t)
	if err := ApplyLedgerMigration(db); err != nil {
		t.Fatalf("ApplyLedgerMigration: %v", err)
	}

	tables := []string{"ticket_ledger", "owner_item", "assumption"}
	for _, tbl := range tables {
		rows, err := db.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", tbl, err)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				t.Errorf("rows.Close: %v", err)
			}
		}()

		found := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if name == "id" && pk == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("table %s missing id primary key", tbl)
		}
	}
}

func TestLedgerMigrationIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := ApplyLedgerMigration(db); err != nil {
		t.Fatalf("first ApplyLedgerMigration: %v", err)
	}
	if err := ApplyLedgerMigration(db); err != nil {
		t.Fatalf("second ApplyLedgerMigration: %v", err)
	}

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('ticket_ledger','owner_item','assumption')").Scan(&count)
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 tables, got %d", count)
	}
}

func TestLedgerMigrationFailureRollsBack(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Exec("CREATE VIEW owner_item AS SELECT 1 AS id")
	if err != nil {
		t.Fatalf("create view: %v", err)
	}

	err = ApplyLedgerMigration(db)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrLedgerMigration) {
		t.Fatalf("expected ErrLedgerMigration, got %v", err)
	}

	tables := []string{"ticket_ledger", "assumption"}
	for _, tbl := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err == nil {
			t.Errorf("table %s should not exist after rollback", tbl)
		}
	}
}
