// Purpose: internal/storage/health_probe_extra_test.go — the branches
//
//	health_probe.go's own package-boundary tests can't reach from
//	storage_test: sweepProbeRows' real sentinel-removal behavior and its
//	own real-database error branch, and createAnchorTable's DDL-failure
//	branch (distinct from its tableExists-failure branch, which
//	health_closeddb_test.go already covers via a fully closed *sql.DB).
//	`package storage` (internal test package) is required here for the
//	same reason retention_helpers_test.go gives: these three functions
//	are unexported, and Go links both `storage` and `storage_test` test
//	files into one binary.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// probeExtraClock is a minimal Clock double, local to this white-box
// file (plugin_migrate_test.go's fixedClock lives in the external
// storage_test package and is not visible here).
type probeExtraClock struct{}

func (probeExtraClock) Now() time.Time { return time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) }

// openProbeExtraDB opens a real modernc-sqlite database at a fresh
// t.TempDir() path and returns both the *sql.DB and its on-disk path.
func openProbeExtraDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// TestSweepProbeRowsRemovesRealSentinels inserts real sentinel rows
// directly (bypassing insertProbeRow, so this doesn't circularly depend
// on the code under test) and asserts sweepProbeRows actually removes
// every one — proving real state change, not merely a nil return.
func TestSweepProbeRowsRemovesRealSentinels(t *testing.T) {
	ctx := context.Background()
	db, _ := openProbeExtraDB(t)
	if _, err := Bootstrap(ctx, db, BootstrapOpts{Clock: probeExtraClock{}}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO `+quoteIdent(healthProbeTable)+` DEFAULT VALUES`); err != nil {
			t.Fatalf("seed sentinel row %d: %v", i, err)
		}
	}
	var before int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(healthProbeTable)).Scan(&before); err != nil {
		t.Fatalf("count before sweep: %v", err)
	}
	if before != 3 {
		t.Fatalf("seeded row count = %d, want 3", before)
	}

	if err := sweepProbeRows(ctx, db); err != nil {
		t.Fatalf("sweepProbeRows: %v", err)
	}

	var after int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(healthProbeTable)).Scan(&after); err != nil {
		t.Fatalf("count after sweep: %v", err)
	}
	if after != 0 {
		t.Fatalf("row count after sweepProbeRows = %d, want 0 (every sentinel removed)", after)
	}
}

// TestSweepProbeRowsErrorsOnClosedDB proves sweepProbeRows surfaces a
// real database error rather than swallowing it.
func TestSweepProbeRowsErrorsOnClosedDB(t *testing.T) {
	db, _ := openProbeExtraDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := sweepProbeRows(context.Background(), db); err == nil {
		t.Fatal("sweepProbeRows against a closed *sql.DB = nil error, want the real database/sql failure")
	}
}

// TestCreateAnchorTableFailsOnReadOnlyDB proves createAnchorTable's DDL
// failure branch: against a read-only reopen of a bootstrapped database,
// tableExists succeeds (reads are allowed) but the CREATE TABLE itself
// fails — a distinct branch from health_closeddb_test.go's fully-closed
// *sql.DB, which fails at tableExists instead.
func TestCreateAnchorTableFailsOnReadOnlyDB(t *testing.T) {
	ctx := context.Background()
	db, path := openProbeExtraDB(t)
	if _, err := Bootstrap(ctx, db, BootstrapOpts{Clock: probeExtraClock{}}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	roDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = roDB.Close() }()

	created, err := createAnchorTable(ctx, roDB, "extra_anchor_probe_test")
	if err == nil {
		t.Fatal("createAnchorTable(new table) against a read-only database = nil error, want the CREATE TABLE failure")
	}
	if created {
		t.Fatal("createAnchorTable reported created=true alongside an error")
	}
}
