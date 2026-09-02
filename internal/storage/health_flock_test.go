// Purpose: StorageHealthCheck's FlockProbe check — skips gracefully
//
//	against an in-memory database (nothing on disk to flock) and detects a
//	currently-held §D-3 exclusive lock from a separate real connection.
//	Split from health_test.go as a sibling file per R-14.117 (Art.10.3
//	300-line cap; mechanical relocation, no behavior change).
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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestStorageHealth_FlockProbe_InMemorySkipped proves the flock-probe
// check reports OK and skips gracefully against an in-memory database,
// where mainDBFilePath resolves to an empty path — there is nothing on
// disk to flock.
func TestStorageHealth_FlockProbe_InMemorySkipped(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open :memory:: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap :memory:: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if !report.FlockProbe.OK {
		t.Errorf("FlockProbe.OK = false for an in-memory database, want true (nothing to flock)")
	}
	if !strings.Contains(report.FlockProbe.Detail, "in-memory") {
		t.Errorf("FlockProbe.Detail = %q, want it to mention in-memory", report.FlockProbe.Detail)
	}
}

// TestStorageHealth_FlockProbe_HeldByAnotherOpen proves the flock-probe
// check detects a currently-held §D-3 exclusive lock: sqlite.Open holds
// the real lock via its own flock_darwin.go/flock_linux.go path, and a
// SEPARATE raw connection's health check against the same file must see
// it as held.
func TestStorageHealth_FlockProbe_HeldByAnotherOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("§D-3 flock is tier-2 (always-refuse) on windows; nothing to hold")
	}
	path := filepath.Join(t.TempDir(), "cascade.db")

	holder, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open (lock holder): %v", err)
	}
	defer func() { _ = holder.Close() }()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open (second connection): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.FlockProbe.OK {
		t.Fatal("FlockProbe.OK = true while sqlite.Open holds the exclusive lock, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.FlockProbe.Err, &hcErr) || hcErr.Check != "flock-probe" {
		t.Errorf("FlockProbe.Err = %v, want *HealthCheckError{Check: flock-probe}", report.FlockProbe.Err)
	}
	if !cascade.HasKind(report.FlockProbe.Err, cascade.KindConflict) {
		t.Errorf("FlockProbe.Err kind: want KindConflict, got %v", report.FlockProbe.Err)
	}
}
