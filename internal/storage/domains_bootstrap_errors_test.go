// Purpose: Bootstrap's error paths — a required-but-nil Clock, a
//
//	already-closed database, and a genuinely-absent ledger table under
//	PRAGMA query_only. Split from domains_test.go as a sibling file per
//	R-14.117 (Art.10.3 300-line cap; mechanical relocation, no behavior
//	change). Every .db file lives in t.TempDir() (Art.7.1).
//
// SPORT: internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestBootstrap_RequiresClock proves Bootstrap refuses a nil Clock rather
// than silently reading the wall clock (which forbidigo would in any case
// forbid at any call site in this package).
func TestBootstrap_RequiresClock(t *testing.T) {
	db := openTestDB(t)
	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{})
	if err == nil {
		t.Fatal("Bootstrap with nil Clock: want error, got nil")
	}
}

// TestBootstrap_FailsOnClosedDB proves Bootstrap propagates a real
// database error (rather than panicking) when db is already closed —
// PRAGMA journal_mode=WAL, the first statement Bootstrap issues, fails
// immediately.
func TestBootstrap_FailsOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))

	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock})
	if err == nil {
		t.Fatal("Bootstrap on a closed database: want error, got nil")
	}
}

// TestBootstrap_FailsWhenLedgerTableCreateErrors drives Bootstrap's
// stampSchemaVersion/ensureLedgerTable CREATE-error branch: after a first
// successful Bootstrap, applied_migrations is dropped (so it is genuinely
// absent, not a no-op re-create) and the connection is switched to
// `PRAGMA query_only = 1`, under which SQLite refuses to create a table
// that does not yet exist with SQLITE_READONLY — verified empirically
// against modernc-sqlite (a CREATE TABLE IF NOT EXISTS on an ALREADY-
// present table is a true no-op under query_only and does not error,
// which is why this test drops the table first rather than reusing an
// existing one). The eleven domain anchors and the health-probe table are
// all already present, so bootstrapDomainTables itself is a no-op and
// Bootstrap fails specifically inside the ledger-stamp step.
func TestBootstrap_FailsWhenLedgerTableCreateErrors(t *testing.T) {
	db := openTestDB(t)
	// PRAGMA query_only is per-connection; database/sql's pool may
	// otherwise hand a later call a different pooled connection than the
	// one query_only was set on, silently defeating this test. Pinning
	// the pool to one connection makes every statement in this test —
	// including Bootstrap's own — share the same underlying connection.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE applied_migrations`); err != nil {
		t.Fatalf("drop applied_migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA query_only = 1;`); err != nil {
		t.Fatalf("set query_only: %v", err)
	}

	_, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock})
	if err == nil {
		t.Fatal("Bootstrap with a genuinely-absent ledger table under PRAGMA query_only=1: want error, got nil")
	}
}
