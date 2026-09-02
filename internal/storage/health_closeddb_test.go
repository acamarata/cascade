// Purpose: StorageHealthCheck against an already-closed *sql.DB — drives
//
//	every check's raw-query-error branch (distinct from their "row/table
//	absent" branches covered elsewhere) with a real database/sql error
//	that is NOT a *mcsqlite.Error. Split from health_test.go as a sibling
//	file per R-14.117 (Art.10.3 300-line cap; mechanical relocation, no
//	behavior change).
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestStorageHealth_ClosedDBFailsEveryQueryDrivenCheck closes the
// underlying *sql.DB before calling StorageHealthCheck, driving every
// check's raw-query-error branch (distinct from their "row/table absent"
// branches, which the earlier tests already cover) with a real
// database/sql error that is NOT a *mcsqlite.Error — exercising
// classifyProbeError's default KindUnavailable fallback for ProbeWrite,
// and proving HealthReport.OK() correctly reports false once at least one
// check fails.
func TestStorageHealth_ClosedDBFailsEveryQueryDrivenCheck(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	report := storage.StorageHealthCheck(context.Background(), db)
	if report.OK() {
		t.Fatal("HealthReport.OK() = true against a closed database, want false")
	}
	for name, res := range report.Results() {
		if name == "flock-probe" {
			// The flock probe never touches db itself — it resolves the
			// on-disk path via a query, which also fails closed, but is
			// asserted separately below for its own message.
			continue
		}
		if res.OK {
			t.Errorf("check %q: OK=true against a closed database, want false", name)
		}
	}
	if report.FlockProbe.OK {
		t.Error("FlockProbe.OK = true against a closed database (path resolution must fail closed too), want false")
	}
}
