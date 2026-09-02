package storage_test

// Purpose: TestImportPreImportHookRefusal — a caller-injected
//
//	ImportOpts.PreImport hook that returns an error refuses the import
//	before any write transaction opens (zero rows written), while a nil
//	hook (the zero value) skips the step entirely and imports normally
//	(acceptance criteria "Pre-import hook refusal").
//
// SPORT: internal.storage.export.ImportOpts/ADDED (P1-E02-W1-S03-T3).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

func sampleImportStream() *strings.Reader {
	return strings.NewReader(
		`{"_type":"header","domain":"queue","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"k","value":"dg=="}` + "\n",
	)
}

// TestImportPreImportHookRefusal proves a failing PreImport hook refuses
// Import before the write transaction opens.
func TestImportPreImportHookRefusal(t *testing.T) {
	db := bootstrappedTestDB(t)
	hookErr := errors.New("simulated pre-import snapshot failure")
	called := false

	report, err := storage.Import(context.Background(), db, storage.DomainQueue, sampleImportStream(), storage.ImportOpts{
		PreImport: func(_ context.Context) error {
			called = true
			return hookErr
		},
	})
	if err == nil {
		t.Fatal("Import with a refusing PreImport hook: want an error, got nil")
	}
	if !called {
		t.Fatal("PreImport hook was never called")
	}
	if !errors.Is(err, hookErr) {
		t.Errorf("Import error %v does not wrap the hook's own error", err)
	}
	if report != (storage.ImportReport{}) {
		t.Errorf("ImportReport = %+v, want zero value", report)
	}
	if n := countKVRows(t, db, string(storage.DomainQueue)); n != 0 {
		t.Errorf("PreImport refusal wrote %d rows, want 0", n)
	}
}

// TestImportPreImportHookRefusal_NilHookSkipsStep proves a nil PreImport
// (ImportOpts's zero value) is not treated as a refusal — Import proceeds
// normally.
func TestImportPreImportHookRefusal_NilHookSkipsStep(t *testing.T) {
	db := bootstrappedTestDB(t)
	report, err := storage.Import(context.Background(), db, storage.DomainQueue, sampleImportStream(), storage.ImportOpts{})
	if err != nil {
		t.Fatalf("Import with nil PreImport: %v", err)
	}
	if report.RowsImported != 1 {
		t.Errorf("RowsImported = %d, want 1", report.RowsImported)
	}
}

// TestImportPreImportHookRefusal_RunsBeforeVersionGuardOrder is a
// documentation-by-test check: the hook runs AFTER the version guard
// (export_import.go's Import doc lists the ordering (a)-(e)) — a version
// mismatch is reported even when the hook would also have refused, and
// the hook is never invoked in that case.
func TestImportPreImportHookRefusal_RunsBeforeVersionGuardOrder(t *testing.T) {
	db := bootstrappedTestDB(t)
	called := false
	stream := strings.NewReader(
		`{"_type":"header","domain":"queue","schema_version":0,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n",
	)
	_, err := storage.Import(context.Background(), db, storage.DomainQueue, stream, storage.ImportOpts{
		PreImport: func(_ context.Context) error { called = true; return nil },
	})
	if err == nil {
		t.Fatal("Import with stale schema_version: want an error, got nil")
	}
	if called {
		t.Error("PreImport hook was called despite the version guard refusing first")
	}
}
