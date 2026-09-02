// Purpose: Bootstrap's creation + idempotency contract (§5.9, Art.2,
//
//	Art.1) — the first call creates every anchor table, WAL mode, and the
//	initial schema_version stamp; the second call is a true no-op that
//	mutates nothing. Split from domains_test.go as a sibling file per
//	R-14.117 (Art.10.3 300-line cap; mechanical relocation, no behavior
//	change). Every .db file lives in t.TempDir() (Art.7.1); every
//	assertion about table presence queries sqlite_master on a real
//	modernc-sqlite database directly — never a self-authored schema
//	double (Art.2).
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestBootstrap_CreatesDomainAnchorTables proves Bootstrap's first call
// against a fresh database creates every AllDomains anchor table plus the
// reserved health-probe table, verified directly against sqlite_master —
// never a self-authored schema double (Art.2).
func TestBootstrap_CreatesDomainAnchorTables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))

	report, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// Ten domain anchors + one reserved health-probe table.
	if report.TablesCreated != 11 {
		t.Errorf("TablesCreated = %d, want 11", report.TablesCreated)
	}

	tables := sqliteMasterTableNames(t, db)
	for _, meta := range storage.AllDomains {
		want := meta.TablePrefix + "_domain_root"
		if !tables[want] {
			t.Errorf("sqlite_master missing anchor table %q for domain %s", want, meta.ID)
		}
	}
	if !tables["applied_migrations"] {
		t.Error("sqlite_master missing applied_migrations ledger table")
	}
	if !tables["__health_probe__"] {
		t.Error("sqlite_master missing __health_probe__ reserved table")
	}

	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode;`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM applied_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != 1 {
		t.Errorf("stamped schema_version = %d, want 1", version)
	}
}

// TestBootstrapIdempotent proves the §5.9 idempotency contract: a second
// Bootstrap call on the same database returns TablesCreated: 0, and every
// domain-anchor table's row count is unchanged (proving no data mutation,
// not merely "no error").
func TestBootstrapIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	opts := storage.BootstrapOpts{Clock: clock}

	if _, err := storage.Bootstrap(ctx, db, opts); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}

	// Write a marker row into one domain's anchor table so the second
	// Bootstrap call has real data to prove it never touches.
	if _, err := db.ExecContext(ctx, `INSERT INTO context_domain_root (id) VALUES (42)`); err != nil {
		t.Fatalf("seed marker row: %v", err)
	}
	before := rowCounts(t, db)

	report, err := storage.Bootstrap(ctx, db, opts)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if report.TablesCreated != 0 {
		t.Errorf("second Bootstrap TablesCreated = %d, want 0", report.TablesCreated)
	}
	if report.Delta == "" {
		t.Error("second Bootstrap Delta is empty, want a zero-delta report")
	}

	after := rowCounts(t, db)
	for table, want := range before {
		if after[table] != want {
			t.Errorf("row count for %s changed: before=%d after=%d (Bootstrap must not mutate data on re-run)", table, want, after[table])
		}
	}

	var stampRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_migrations WHERE schema_version = 1`).Scan(&stampRows); err != nil {
		t.Fatalf("count stamp rows: %v", err)
	}
	if stampRows != 1 {
		t.Errorf("applied_migrations has %d rows at schema_version=1 after two Bootstrap calls, want exactly 1 (no duplicate stamp)", stampRows)
	}
}
