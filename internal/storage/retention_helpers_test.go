// Purpose: shared test helpers for the retention_*_test.go split (this
//
//	file, retention_prune_test.go, retention_isolation_test.go,
//	retention_vacuum_test.go) — opening a real modernc-sqlite database in
//	t.TempDir() (Art.7.1), creating a minimal timestamped table, and the
//	row-count/report-lookup assertions every sibling file uses. Split out
//	as its own file (R-14.117: Art.10.3's 300-line cap caught the
//	original single retention_test.go at 613 lines; this is a mechanical
//	relocation, no behavior change).
//
// Constraints: this file is `package storage` (internal test package),
//
//	deliberately alongside the rest of this package's `storage_test`
//	(external) test files — Go links both into one test binary. The
//	deviation is intentional and narrow: TestPruneBatchCap
//	(retention_prune_test.go) needs the "write-executor call-count
//	instrumentation" the ticket's own acceptance criteria demands, and
//	the only honest way to count DELETE round-trips against a REAL
//	engine (never a mock) is to wrap the unexported dbExecer seam
//	directly — an external test package cannot reach it. Every other
//	test across this split exercises DomainPruner.Prune / VacuumJob.Run
//	purely through their exported signatures, exactly as the
//	storage_test files do.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED,
//
//	internal.storage.retention.VacuumJob/ADDED,
//	internal.storage.retention.RetentionConfig/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// openRetentionTestDB opens a real modernc-sqlite database at a fresh
// t.TempDir() path in WAL mode (mirroring domains.go's Bootstrap and
// providers/sqlite.Open, both of which run WAL, so VacuumJob's
// wal_checkpoint has something real to do), closing it at cleanup.
func openRetentionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		t.Fatalf("set WAL mode: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// createTSTable creates a minimal (id, ts INTEGER, filler TEXT) table —
// ts is the Unix-seconds row-timestamp column a PruneTarget names; filler
// gives TestVacuumJob a real, non-trivial on-disk row size to reclaim.
func createTSTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	ddl := fmt.Sprintf(`CREATE TABLE %s (id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, filler TEXT)`, quoteIdent(name))
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create table %s: %v", name, err)
	}
}

// insertTSRow inserts one row with the given Unix-seconds timestamp.
func insertTSRow(t *testing.T, db *sql.DB, table string, ts int64, filler string) {
	t.Helper()
	stmt := fmt.Sprintf(`INSERT INTO %s (ts, filler) VALUES (?, ?)`, quoteIdent(table))
	if _, err := db.Exec(stmt, ts, filler); err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
}

// countRows returns table's current row count.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoteIdent(table))).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// findReport returns the PruneReport for domain, failing the test if
// absent.
func findReport(t *testing.T, reports []PruneReport, domain DomainID) PruneReport {
	t.Helper()
	for _, r := range reports {
		if r.Domain == domain {
			return r
		}
	}
	t.Fatalf("no PruneReport for domain %s", domain)
	return PruneReport{}
}
