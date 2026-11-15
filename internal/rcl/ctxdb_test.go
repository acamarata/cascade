package rcl

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCtxDBCreatesSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "test.db")

	// Invalid paths
	if _, err := OpenCtxDB(""); err != ErrCtxDBInvalid {
		t.Fatalf("expected ErrCtxDBInvalid for empty, got %v", err)
	}
	if _, err := OpenCtxDB(" "); err != ErrCtxDBInvalid {
		t.Fatalf("expected ErrCtxDBInvalid for space, got %v", err)
	}
	if _, err := OpenCtxDB("relative/path"); err != ErrCtxDBInvalid {
		t.Fatalf("expected ErrCtxDBInvalid for relative, got %v", err)
	}
	if _, err := OpenCtxDB("~/relative"); err != ErrCtxDBInvalid {
		t.Fatalf("expected ErrCtxDBInvalid for tilde, got %v", err)
	}

	db, err := OpenCtxDB(dbPath)
	if err != nil {
		t.Fatalf("OpenCtxDB failed: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("expected parent dir perm 0700, got %v", info.Mode().Perm())
	}

	tables := []string{"segments", "blobs", "sections"}
	for _, tbl := range tables {
		var name string
		if err := db.DB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
		var ftsName string
		if err := db.DB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl+"_fts").Scan(&ftsName); err != nil {
			t.Fatalf("FTS table %s_fts missing: %v", tbl, err)
		}
	}

	for _, tbl := range tables {
		rows, err := db.DB.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			t.Fatalf("pragma table_info(%s): %v", tbl, err)
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				t.Fatalf("scan row: %v", err)
			}
			if name == "id" && (ctype != "INTEGER" || pk != 1) {
				t.Fatalf("id column invalid in %s", tbl)
			}
			if name == "external_id" && (ctype != "TEXT" || notnull != 1) {
				t.Fatalf("external_id column invalid in %s", tbl)
			}
			if name == "text" && (ctype != "TEXT" || notnull != 1) {
				t.Fatalf("text column invalid in %s", tbl)
			}
		}
		rows.Close()
	}

	db2, err := OpenCtxDB(dbPath)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close db2: %v", err)
	}

	var nilDb *CtxDB
	if err := nilDb.Close(); err != ErrCtxDBInvalid {
		t.Fatalf("nil db Close should return ErrCtxDBInvalid, got %v", err)
	}
	emptyDb := &CtxDB{}
	if err := emptyDb.Close(); err != ErrCtxDBInvalid {
		t.Fatalf("empty db Close should return ErrCtxDBInvalid, got %v", err)
	}
}
