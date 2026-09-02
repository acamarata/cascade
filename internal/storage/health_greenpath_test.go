// Purpose: StorageHealthCheck's green path plus the Art.1 no-silent-pass
//
//	invariant and HealthCheckError's Error()/Unwrap correctness. Split
//	from health_test.go as a sibling file per R-14.117 (Art.10.3 300-line
//	cap; mechanical relocation, no behavior change). Every .db file lives
//	in t.TempDir() (Art.7.1); every assertion queries a real
//	modernc-sqlite database directly.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestStorageHealth_GreenPath proves every check reports OK against a
// freshly bootstrapped real database — no hard-coded pass: this test only
// passes because Bootstrap genuinely created WAL mode, the stamp row,
// every domain table, and the health-probe table the checks actually
// query for.
func TestStorageHealth_GreenPath(t *testing.T) {
	db, _ := bootstrappedDB(t)
	report := storage.StorageHealthCheck(context.Background(), db)

	for name, res := range report.Results() {
		if !res.OK {
			t.Errorf("check %q: OK=false, Detail=%q, Err=%v", name, res.Detail, res.Err)
		}
	}
	if !report.OK() {
		t.Error("HealthReport.OK() = false on a freshly bootstrapped database")
	}
}

// TestStorageHealth_ReportDoesNotSilentlyPass proves no CheckResult ever
// carries OK:false with a nil Err (Art.1: "no result returns (nil, nil)
// when a check fails").
func TestStorageHealth_ReportDoesNotSilentlyPass(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE audit_domain_root`); err != nil {
		t.Fatalf("drop audit_domain_root: %v", err)
	}
	report := storage.StorageHealthCheck(context.Background(), db)
	for name, res := range report.Results() {
		if !res.OK && res.Err == nil {
			t.Errorf("check %q: OK=false but Err=nil (Art.1 forbids a silent failure)", name)
		}
	}
}

// TestHealthCheckError_ErrorString proves HealthCheckError.Error() names
// both the failing check and the wrapped taxonomy error, and that
// errors.As still recovers the underlying *cascade.Error through it
// (Unwrap correctness) so a caller can get the Kind either way.
func TestHealthCheckError_ErrorString(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE queue_domain_root`); err != nil {
		t.Fatalf("drop queue_domain_root: %v", err)
	}
	report := storage.StorageHealthCheck(context.Background(), db)

	if report.DomainTables.Err == nil {
		t.Fatal("DomainTables.Err is nil after dropping a domain table")
	}
	msg := report.DomainTables.Err.Error()
	if !strings.Contains(msg, "domain-tables") {
		t.Errorf("HealthCheckError.Error() = %q, want it to name the failing check (domain-tables)", msg)
	}

	var cerr *cascade.Error
	if !errors.As(report.DomainTables.Err, &cerr) {
		t.Fatalf("errors.As(DomainTables.Err, *cascade.Error) failed — Unwrap must expose the taxonomy error")
	}
	if cerr.Kind != cascade.KindIntegrity {
		t.Errorf("recovered cascade.Error.Kind = %v, want KindIntegrity", cerr.Kind)
	}
}
