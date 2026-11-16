package rcl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func countRows(t *testing.T, db *CtxDB, table string) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestCtxDBInvalidAndDuplicate(t *testing.T) {
	badPaths := []string{"", "   ", "\t\n", "relative/path.db", "~/some/path.db"}
	for _, p := range badPaths {
		db, err := OpenCtxDB(p)
		if err == nil || !errors.Is(err, ErrCtxDBInvalid) || db != nil {
			t.Fatalf("OpenCtxDB(%q) = %v, %v; want nil, ErrCtxDBInvalid", p, db, err)
		}
		if p != "" && strings.TrimSpace(p) != "" {
			if _, statErr := os.Stat(p); statErr == nil {
				t.Fatalf("OpenCtxDB(%q) created a file", p)
			}
		}
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ctx.db")
	db, err := OpenCtxDB(dbPath)
	if err != nil {
		t.Fatalf("OpenCtxDB(%q) = %v", dbPath, err)
	}
	defer db.Close()

	var nilDB *CtxDB
	if err := nilDB.Insert("segment", "id", "text"); !errors.Is(err, ErrCtxDBInvalid) {
		t.Fatalf("nil receiver Insert = %v; want ErrCtxDBInvalid", err)
	}

	snapMain := map[string]int{"segments": countRows(t, db, "segments"), "blobs": countRows(t, db, "blobs"), "sections": countRows(t, db, "sections")}
	snapFTS := map[string]int{"segments_fts": countRows(t, db, "segments_fts"), "blobs_fts": countRows(t, db, "blobs_fts"), "sections_fts": countRows(t, db, "sections_fts")}

	cases := []struct{ typ, id, text string }{
		{"widget", "x", "y"}, {"segment", "", "y"}, {"segment", "   ", "y"},
		{"segment", "x", ""}, {"segment", "x", "   "}, {"segment", "", ""},
	}
	for _, c := range cases {
		if err := db.Insert(c.typ, c.id, c.text); !errors.Is(err, ErrCtxDBInvalid) {
			t.Fatalf("Insert(%q,%q,%q) = %v; want ErrCtxDBInvalid", c.typ, c.id, c.text, err)
		}
	}

	for k, v := range snapMain {
		if got := countRows(t, db, k); got != v {
			t.Fatalf("main table %s count = %d; want %d", k, got, v)
		}
	}
	for k, v := range snapFTS {
		if got := countRows(t, db, k); got != v {
			t.Fatalf("FTS table %s count = %d; want %d", k, got, v)
		}
	}

	if err := db.Insert("segment", "seg-1", "alpha bravo"); err != nil {
		t.Fatalf("Insert segment seg-1: %v", err)
	}
	if got := countRows(t, db, "segments"); got != 1 {
		t.Fatalf("segments count = %d; want 1", got)
	}

	snapMain2 := map[string]int{"segments": 1, "blobs": 0, "sections": 0}
	snapFTS2 := map[string]int{"segments_fts": 1, "blobs_fts": 0, "sections_fts": 0}
	if err := db.Insert("segment", "seg-1", "different"); !errors.Is(err, ErrCtxDBDuplicate) {
		t.Fatalf("duplicate insert = %v; want ErrCtxDBDuplicate", err)
	}
	for k, v := range snapMain2 {
		if got := countRows(t, db, k); got != v {
			t.Fatalf("main %s count = %d; want %d", k, got, v)
		}
	}
	for k, v := range snapFTS2 {
		if got := countRows(t, db, k); got != v {
			t.Fatalf("FTS %s count = %d; want %d", k, got, v)
		}
	}

	if err := db.Insert("blob", "seg-1", "binary"); err != nil {
		t.Fatalf("cross-type insert: %v", err)
	}
	if got := countRows(t, db, "blobs"); got != 1 {
		t.Fatalf("blobs count = %d; want 1", got)
	}
	if got := countRows(t, db, "segments"); got != 1 {
		t.Fatalf("segments count = %d; want 1", got)
	}

	rows, err := db.DB.Query("SELECT rowid FROM segments_fts WHERE segments_fts MATCH ?", "zzzznevermatches")
	if err != nil {
		t.Fatalf("no-match query: %v", err)
	}
	defer rows.Close()
	var hits []int
	for rows.Next() {
		var r int
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		hits = append(hits, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if hits == nil {
		hits = []int{}
	}
	if len(hits) != 0 {
		t.Fatalf("no-match search returned %d rows; want 0", len(hits))
	}
}
