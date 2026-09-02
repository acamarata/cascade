package storage_test

// Purpose: TestImportVersionGuard — an export whose header.SchemaVersion
//
//	is older than the target's on-disk schema_version stamp returns a
//	typed *storage.ImportVersionError and writes zero rows, verified by
//	direct COUNT after the attempted import (Art.2, acceptance criteria
//	"Schema version guard").
//
// SPORT: internal.storage.export.ImportVersionError/ADDED (P1-E02-W1-S03-T3).

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestImportVersionGuard builds a header line claiming schema_version 0
// (older than Bootstrap's stamp of 1) and asserts Import refuses with a
// typed ImportVersionError before writing anything.
func TestImportVersionGuard(t *testing.T) {
	db := bootstrappedTestDB(t) // stamps schema_version = 1 (domains.go's bootstrapSchemaVersion).

	stream := strings.NewReader(
		`{"_type":"header","domain":"config","schema_version":0,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"k","value":"dg=="}` + "\n",
	)

	report, err := storage.Import(context.Background(), db, storage.DomainConfig, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import: want ImportVersionError, got nil")
	}
	var verr *storage.ImportVersionError
	if !errors.As(err, &verr) {
		t.Fatalf("Import error %v (%T) is not a *storage.ImportVersionError", err, err)
	}
	if verr.GotVersion != 0 {
		t.Errorf("GotVersion = %d, want 0", verr.GotVersion)
	}
	if verr.MinVersion != 1 {
		t.Errorf("MinVersion = %d, want 1", verr.MinVersion)
	}
	if report != (storage.ImportReport{}) {
		t.Errorf("ImportReport = %+v, want zero value", report)
	}
	if n := countKVRows(t, db, string(storage.DomainConfig)); n != 0 {
		t.Errorf("version-guard refusal wrote %d rows, want 0", n)
	}
}

// TestImportVersionGuard_EqualVersionAllowed proves the guard is a floor
// ("older than"), not an equality requirement: header.SchemaVersion equal
// to the target's stamp is accepted.
func TestImportVersionGuard_EqualVersionAllowed(t *testing.T) {
	db := bootstrappedTestDB(t)
	stream := strings.NewReader(
		`{"_type":"header","domain":"config","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"k","value":"dg=="}` + "\n",
	)
	report, err := storage.Import(context.Background(), db, storage.DomainConfig, stream, storage.ImportOpts{})
	if err != nil {
		t.Fatalf("Import (equal schema_version): %v", err)
	}
	if report.RowsImported != 1 {
		t.Errorf("RowsImported = %d, want 1", report.RowsImported)
	}
}

// TestImportVersionGuard_UnboostrappedTarget proves Import refuses (rather
// than panicking or silently defaulting) against a target database that
// was never Bootstrapped at all — no applied_migrations table to read a
// floor from.
func TestImportVersionGuard_UnboostrappedTarget(t *testing.T) {
	db := openTestDB(t) // deliberately NOT bootstrapped.
	stream := bytes.NewReader([]byte(
		`{"_type":"header","domain":"config","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n",
	))
	_, err := storage.Import(context.Background(), db, storage.DomainConfig, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import against an un-bootstrapped target: want an error, got nil")
	}
}
