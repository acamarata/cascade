package economics

// Purpose: shared reservation-suite test fixtures: a real, file-backed
//   modernc-sqlite database per t.TempDir (Art.2/Art.7.1 -- never an
//   in-memory self-authored double), migrated via
//   ApplyReservationSchema, plus a base ReserveRequest builder.
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this file's tests

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// openReservationTestDB opens a real modernc-sqlite database file under
// t.TempDir() and applies the reservation MigrationSet.
func openReservationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reservation-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ApplyReservationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyReservationSchema: %v", err)
	}
	return db
}

// newReservationTestStore opens a fresh test db and wraps it in a
// ReservationStore over the fixed test clock.
func newReservationTestStore(t *testing.T) *ReservationStore {
	t.Helper()
	db := openReservationTestDB(t)
	store, err := NewReservationStore(db, newTestClock())
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	return store
}

// baseReserveRequest returns a minimally-valid interactive ReserveRequest
// for tests to mutate.
func baseReserveRequest() ReserveRequest {
	est, _ := EstimateFor(1000, conductor.TaskClassChat, 1)
	return ReserveRequest{
		JobID: "job-1", ProjectID: "project-1", LaneID: "lane-1",
		DomainID: "domain-1", ScopeID: "scope-1", Kind: ReservationInteractive,
		Estimate: est, BasePrice: 1.0, ScopeGlobs: []string{"**"},
	}
}
