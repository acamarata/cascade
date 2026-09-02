// Purpose: StorageHealthCheck's DomainTables and WALMode failure triggers
//
//	— a dropped domain anchor table, and a database explicitly switched
//	back off WAL after Bootstrap. Split from health_test.go as a sibling
//	file per R-14.117 (Art.10.3 300-line cap; mechanical relocation, no
//	behavior change). Every .db file lives in t.TempDir() (Art.7.1).
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestStorageHealth_MissingDomainTable drops one domain's anchor table and
// asserts DomainTables flags it via a typed *HealthCheckError, and that
// sqlite_master is what the check actually consulted (not an in-memory
// cache of AllDomains presence).
func TestStorageHealth_MissingDomainTable(t *testing.T) {
	db, _ := bootstrappedDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `DROP TABLE memory_domain_root`); err != nil {
		t.Fatalf("drop memory_domain_root: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.DomainTables.OK {
		t.Fatal("DomainTables.OK = true after dropping a domain table, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.DomainTables.Err, &hcErr) {
		t.Fatalf("DomainTables.Err = %v (%T), want *storage.HealthCheckError", report.DomainTables.Err, report.DomainTables.Err)
	}
	if hcErr.Check != "domain-tables" {
		t.Errorf("HealthCheckError.Check = %q, want domain-tables", hcErr.Check)
	}
	if !cascade.HasKind(report.DomainTables.Err, cascade.KindIntegrity) {
		t.Errorf("DomainTables.Err kind: want KindIntegrity, got %v", report.DomainTables.Err)
	}

	// Every other check must remain unaffected by the one dropped table.
	if !report.WALMode.OK {
		t.Errorf("WALMode.OK = false, want true (unaffected by a dropped domain table)")
	}
}

// TestStorageHealth_NonWALJournalMode opens a database explicitly switched
// to DELETE journal mode and asserts WALMode flags it.
func TestStorageHealth_NonWALJournalMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatalf("set journal_mode=DELETE: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	// Bootstrap itself sets WAL — to exercise a genuinely non-WAL
	// database, bootstrap first, then explicitly switch back off WAL.
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatalf("re-set journal_mode=DELETE after Bootstrap: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.WALMode.OK {
		t.Fatal("WALMode.OK = true with journal_mode=DELETE, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.WALMode.Err, &hcErr) || hcErr.Check != "wal-mode" {
		t.Errorf("WALMode.Err = %v, want *HealthCheckError{Check: wal-mode}", report.WALMode.Err)
	}
}
