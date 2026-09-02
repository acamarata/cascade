// Purpose: shared test helpers for the domains_*_test.go split — opening a
//
//	real modernc-sqlite database in t.TempDir() (Art.7.1), reading
//	sqlite_master's table list as Art.2 ground truth, and counting rows
//	across every domain anchor table. Split from domains_test.go as a
//	sibling file per R-14.117 (Art.10.3 300-line cap; mechanical
//	relocation, no behavior change) so every domains_*_test.go file can
//	call these without duplicating them.
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage"
)

// openTestDB opens a real modernc-sqlite database at a fresh t.TempDir()
// path, closing it automatically at test cleanup.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sqliteMasterTableNames returns every table name sqlite_master reports —
// the Art.2 ground truth every test in this file checks Bootstrap's
// claims against.
func sqliteMasterTableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return names
}

// rowCounts returns every domain anchor table's row count, keyed by table
// name, plus the health-probe and ledger tables.
func rowCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	tables := []string{"applied_migrations", "__health_probe__"}
	for _, meta := range storage.AllDomains {
		tables = append(tables, meta.TablePrefix+"_domain_root")
	}
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM "`+table+`"`).Scan(&n); err != nil {
			t.Fatalf("count rows in %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}
