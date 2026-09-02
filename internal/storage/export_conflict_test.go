package storage_test

// Purpose: TestImportConflictStrategies — all three ConflictStrategy
//
//	branches against a colliding key: Skip leaves the existing value
//	intact and increments RowsSkipped; Overwrite replaces it and
//	increments RowsOverwritten; Error returns a typed collision error and
//	writes zero (acceptance criteria "Conflict strategies").
//
// SPORT: internal.storage.export.ImportOpts/ADDED (P1-E02-W1-S03-T3).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

func collidingImportStream(domain, key, newValueB64 string) *strings.Reader {
	return strings.NewReader(
		`{"_type":"header","domain":"` + domain + `","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"` + key + `","value":"` + newValueB64 + `"}` + "\n",
	)
}

// TestImportConflictStrategies_Skip proves ConflictStrategySkip leaves the
// existing row's value untouched and increments RowsSkipped.
func TestImportConflictStrategies_Skip(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedKVRow(t, db, string(storage.DomainRetrieval), "k", []byte("original"))

	report, err := storage.Import(context.Background(), db, storage.DomainRetrieval,
		collidingImportStream("retrieval", "k", "bmV3"), // "new"
		storage.ImportOpts{ConflictStrategy: storage.ConflictStrategySkip},
	)
	if err != nil {
		t.Fatalf("Import (Skip): %v", err)
	}
	if report.RowsSkipped != 1 || report.RowsImported != 0 || report.RowsOverwritten != 0 {
		t.Errorf("report = %+v, want {RowsSkipped:1}", report)
	}
	got, _ := readKVRow(t, db, string(storage.DomainRetrieval), "k")
	if string(got) != "original" {
		t.Errorf("value after Skip = %q, want %q (untouched)", got, "original")
	}
}

// TestImportConflictStrategies_Overwrite proves ConflictStrategyOverwrite
// replaces the existing row's value and increments RowsOverwritten.
func TestImportConflictStrategies_Overwrite(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedKVRow(t, db, string(storage.DomainRetrieval), "k", []byte("original"))

	report, err := storage.Import(context.Background(), db, storage.DomainRetrieval,
		collidingImportStream("retrieval", "k", "bmV3"), // "new"
		storage.ImportOpts{ConflictStrategy: storage.ConflictStrategyOverwrite},
	)
	if err != nil {
		t.Fatalf("Import (Overwrite): %v", err)
	}
	if report.RowsOverwritten != 1 || report.RowsImported != 0 || report.RowsSkipped != 0 {
		t.Errorf("report = %+v, want {RowsOverwritten:1}", report)
	}
	got, _ := readKVRow(t, db, string(storage.DomainRetrieval), "k")
	if string(got) != "new" {
		t.Errorf("value after Overwrite = %q, want %q", got, "new")
	}
}

// TestImportConflictStrategies_Error proves the default ConflictStrategy
// (the zero value, ConflictStrategyError) returns a typed KindConflict
// error and writes zero rows on collision — and that a non-colliding key
// in the SAME stream is also rolled back (atomicity composes with
// conflict handling, not just with parse errors).
func TestImportConflictStrategies_Error(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedKVRow(t, db, string(storage.DomainRetrieval), "k", []byte("original"))

	stream := strings.NewReader(
		`{"_type":"header","domain":"retrieval","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"brand-new","value":"dg=="}` + "\n" +
			`{"_type":"row","key":"k","value":"bmV3"}` + "\n",
	)

	report, err := storage.Import(context.Background(), db, storage.DomainRetrieval, stream, storage.ImportOpts{}) // zero value = Error
	if err == nil {
		t.Fatal("Import (Error strategy, collision): want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("Import error kind = %v (ok=%v), want KindConflict", kind, ok)
	}
	if report != (storage.ImportReport{}) {
		t.Errorf("ImportReport = %+v, want zero value", report)
	}

	// The colliding key's original value survives untouched, and the
	// non-colliding row from the SAME stream was never committed either.
	got, _ := readKVRow(t, db, string(storage.DomainRetrieval), "k")
	if string(got) != "original" {
		t.Errorf("value after refused Error-strategy import = %q, want %q", got, "original")
	}
	if _, exists := readKVRow(t, db, string(storage.DomainRetrieval), "brand-new"); exists {
		t.Error("non-colliding row from the same stream was written despite the collision refusal")
	}
}

// TestImportConflictStrategies_DefaultIsError proves ImportOpts{}'s zero
// value behaves identically to an explicit ConflictStrategyError, per this
// ticket's documented design decision.
func TestImportConflictStrategies_DefaultIsError(t *testing.T) {
	if storage.ConflictStrategy(0) != storage.ConflictStrategyError {
		t.Fatalf("ConflictStrategy zero value = %v, want ConflictStrategyError", storage.ConflictStrategy(0))
	}
}
