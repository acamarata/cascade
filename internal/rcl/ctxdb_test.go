package rcl

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// invalidPathCase holds a single invalid-path subtest for OpenCtxDB.
type invalidPathCase struct {
	name string
	path string
}

// invalidPathCases is the package-level table of invalid path inputs.
var invalidPathCases = []invalidPathCase{
	{name: "empty", path: ""},
	{name: "space", path: " "},
	{name: "relative", path: "relative/path"},
	{name: "tilde", path: "~/relative"},
}

// testInvalidPaths verifies that OpenCtxDB rejects every invalid path.
func testInvalidPaths(t *testing.T) {
	t.Helper()
	for _, tc := range invalidPathCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := OpenCtxDB(tc.path); err != ErrCtxDBInvalid {
				t.Fatalf("expected ErrCtxDBInvalid for %s, got %v", tc.name, err)
			}
		})
	}
}

// testParentDirPerm verifies the parent directory is created with 0700 permissions.
func testParentDirPerm(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if runtime.GOOS == "windows" {
		return // Windows has no POSIX permission bits; the directory existing is the check there
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("expected parent dir perm 0700, got %v", info.Mode().Perm())
	}
}

// testTablesExist verifies that each required table and its FTS virtual table exist.
func testTablesExist(t *testing.T, db *CtxDB) {
	t.Helper()
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
}

// testTableColumns verifies column definitions via PRAGMA table_info.
func testTableColumns(t *testing.T, db *CtxDB) {
	t.Helper()
	tables := []string{"segments", "blobs", "sections"}
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
		if err := rows.Close(); err != nil {
			t.Fatalf("close rows: %v", err)
		}
	}
}

// testReopen verifies that opening an existing database is idempotent.
func testReopen(t *testing.T, dbPath string) {
	t.Helper()
	db2, err := OpenCtxDB(dbPath)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close db2: %v", err)
	}
}

// testNilClose verifies that Close on nil or empty CtxDB returns ErrCtxDBInvalid.
func testNilClose(t *testing.T) {
	t.Helper()
	var nilDb *CtxDB
	if err := nilDb.Close(); err != ErrCtxDBInvalid {
		t.Fatalf("nil db Close should return ErrCtxDBInvalid, got %v", err)
	}
	emptyDb := &CtxDB{}
	if err := emptyDb.Close(); err != ErrCtxDBInvalid {
		t.Fatalf("empty db Close should return ErrCtxDBInvalid, got %v", err)
	}
}

// TestCtxDBCreatesSchema tests schema creation, validation, and edge cases.
func TestCtxDBCreatesSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "test.db")

	testInvalidPaths(t)

	db, err := OpenCtxDB(dbPath)
	if err != nil {
		t.Fatalf("OpenCtxDB failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	testParentDirPerm(t, filepath.Dir(dbPath))
	testTablesExist(t, db)
	testTableColumns(t, db)
	testReopen(t, dbPath)
	testNilClose(t)
}
