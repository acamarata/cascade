// Purpose: shared test helper for the health_*_test.go split — opens a
//
//	fresh real modernc-sqlite database in t.TempDir() (Art.7.1) and
//	Bootstraps it, returning both the *sql.DB and the on-disk path (needed
//	by the flock-probe and read-only tests, which reopen the same file).
//	Split from health_test.go as a sibling file per R-14.117 (Art.10.3
//	300-line cap; mechanical relocation, no behavior change) so every
//	health_*_test.go file can call it without duplicating it.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// bootstrappedDB opens a fresh real modernc-sqlite database in t.TempDir()
// and Bootstraps it, returning the *sql.DB and the on-disk path (needed
// by the flock-probe and read-only tests, which reopen the same file).
func bootstrappedDB(t *testing.T) (db *sql.DB, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "cascade.db")
	sdb, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(context.Background(), sdb, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return sdb, path
}
