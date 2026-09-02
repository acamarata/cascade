// Purpose: StorageHealthCheck's ProbeWrite and SchemaVersion failure
//
//	triggers — a read-only reopen of a bootstrapped database, a
//	never-bootstrapped database (missing ledger), and a stamped
//	schema_version rewritten below the floor. Split from health_test.go
//	as a sibling file per R-14.117 (Art.10.3 300-line cap; mechanical
//	relocation, no behavior change). Every .db file lives in t.TempDir()
//	(Art.7.1).
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestStorageHealth_ProbeWriteFailsOnReadOnlyDB opens the SAME bootstrapped
// database file a second time through a read-only DSN and asserts
// ProbeWrite is flagged — the round-trip genuinely attempts a write and
// genuinely fails, rather than being told the database is read-only.
func TestStorageHealth_ProbeWriteFailsOnReadOnlyDB(t *testing.T) {
	_, path := bootstrappedDB(t)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat bootstrapped db before read-only reopen: %v", err)
	}
	roDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = roDB.Close() }()

	report := storage.StorageHealthCheck(context.Background(), roDB)
	if report.ProbeWrite.OK {
		t.Fatal("ProbeWrite.OK = true against a read-only database, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.ProbeWrite.Err, &hcErr) || hcErr.Check != "probe-write" {
		t.Errorf("ProbeWrite.Err = %v, want *HealthCheckError{Check: probe-write}", report.ProbeWrite.Err)
	}
}

// TestStorageHealth_SchemaVersionMissingLedger proves the schema-version
// check fails cleanly (not a panic, not a false OK) against a database
// that was never bootstrapped at all.
func TestStorageHealth_SchemaVersionMissingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	report := storage.StorageHealthCheck(context.Background(), db)
	if report.SchemaVersion.OK {
		t.Fatal("SchemaVersion.OK = true against a never-bootstrapped database, want false")
	}
	if report.DomainTables.OK {
		t.Fatal("DomainTables.OK = true against a never-bootstrapped database, want false")
	}
	if report.ProbeWrite.OK {
		t.Fatal("ProbeWrite.OK = true against a never-bootstrapped database, want false")
	}
}

// TestStorageHealth_SchemaVersionBelowFloor directly rewrites the stamped
// schema_version to a value below minimumReaderVersion (0) and asserts
// the check fails with the "below floor" branch, distinct from the
// "ledger table missing entirely" branch TestStorageHealth_
// SchemaVersionMissingLedger already covers.
func TestStorageHealth_SchemaVersionBelowFloor(t *testing.T) {
	db, _ := bootstrappedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE applied_migrations SET schema_version = 0`); err != nil {
		t.Fatalf("rewrite schema_version: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.SchemaVersion.OK {
		t.Fatal("SchemaVersion.OK = true with schema_version rewritten below the floor, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.SchemaVersion.Err, &hcErr) || hcErr.Check != "schema-version" {
		t.Errorf("SchemaVersion.Err = %v, want *HealthCheckError{Check: schema-version}", report.SchemaVersion.Err)
	}
}
