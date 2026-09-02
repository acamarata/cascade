package storage_test

// Purpose: coverage-completing edge cases beyond the ticket's named
//
//	acceptance-criteria tests: the header-parse failure branches
//	(empty stream, wrong _type), Export's own failure branches (closed
//	database, unbootstrapped target, a failing io.Writer), and the two
//	small exported-surface methods (ImportVersionError.Error/Unwrap,
//	ConflictStrategy.String) that the named tests exercise only via
//	errors.As/direct field access, never their own method bodies.
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED,
//	internal.storage.export.ImportVersionError/ADDED (P1-E02-W1-S03-T3).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestImport_EmptyStream proves an empty reader (no header line at all)
// is refused with a clear KindIntegrity error, not an EOF that looks like
// success.
func TestImport_EmptyStream(t *testing.T) {
	db := bootstrappedTestDB(t)
	_, err := storage.Import(context.Background(), db, storage.DomainContext, strings.NewReader(""), storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import of an empty stream: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("empty-stream error kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestImport_HeaderTypeMismatch proves a first line that is valid JSON but
// carries the wrong "_type" (a row line, or an unrecognized value) is
// refused rather than silently treated as the header.
func TestImport_HeaderTypeMismatch(t *testing.T) {
	db := bootstrappedTestDB(t)
	stream := strings.NewReader(`{"_type":"row","key":"k","value":"dg=="}` + "\n")
	_, err := storage.Import(context.Background(), db, storage.DomainContext, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import with a non-header first line: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("header-type-mismatch error kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestImport_HeaderNotJSON proves a first line that is not valid JSON at
// all is refused with a KindIntegrity error (the parse-failure branch, as
// opposed to the type-mismatch branch above).
func TestImport_HeaderNotJSON(t *testing.T) {
	db := bootstrappedTestDB(t)
	stream := strings.NewReader("this is not json\n")
	_, err := storage.Import(context.Background(), db, storage.DomainContext, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import with a non-JSON first line: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("header-not-json error kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestImportVersionError_ErrorAndUnwrap exercises ImportVersionError's own
// Error() and Unwrap() method bodies directly (the acceptance-criteria
// test only ever reaches it via errors.As).
func TestImportVersionError_ErrorAndUnwrap(t *testing.T) {
	db := bootstrappedTestDB(t)
	stream := strings.NewReader(
		`{"_type":"header","domain":"context","schema_version":0,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n",
	)
	_, err := storage.Import(context.Background(), db, storage.DomainContext, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("want an error")
	}
	if msg := err.Error(); msg == "" {
		t.Error("ImportVersionError.Error() returned an empty string")
	}
	if errors.Unwrap(err) == nil {
		t.Error("ImportVersionError.Unwrap() returned nil, want the wrapped taxonomy error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("ImportVersionError kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestConflictStrategy_String exercises every ConflictStrategy.String()
// branch, including an out-of-range value (the default branch).
func TestConflictStrategy_String(t *testing.T) {
	cases := map[storage.ConflictStrategy]string{
		storage.ConflictStrategyError:     "Error",
		storage.ConflictStrategySkip:      "Skip",
		storage.ConflictStrategyOverwrite: "Overwrite",
		storage.ConflictStrategy(99):      "ConflictStrategy(99)",
	}
	for strategy, want := range cases {
		if got := strategy.String(); got != want {
			t.Errorf("ConflictStrategy(%d).String() = %q, want %q", int(strategy), got, want)
		}
	}
}

// TestExport_ClosedDatabase proves Export reports a clear error (never a
// panic) when db.BeginTx itself fails.
func TestExport_ClosedDatabase(t *testing.T) {
	db := bootstrappedTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	err := storage.Export(context.Background(), db, storage.DomainContext, &discardWriter{})
	if err == nil {
		t.Fatal("Export against a closed database: want an error, got nil")
	}
}

// TestExport_UnbootstrappedTarget proves Export refuses a database that
// was never Bootstrapped (no applied_migrations table to read a
// schema_version from), mirroring TestImportVersionGuard_UnboostrappedTarget
// on the Export side.
func TestExport_UnbootstrappedTarget(t *testing.T) {
	db := openTestDB(t) // deliberately not bootstrapped.
	err := storage.Export(context.Background(), db, storage.DomainContext, &discardWriter{})
	if err == nil {
		t.Fatal("Export against an un-bootstrapped database: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("unbootstrapped-target error kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestExport_WriteFailure proves a failing io.Writer surfaces as a
// KindUnavailable error rather than being swallowed.
func TestExport_WriteFailure(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedKVRow(t, db, string(storage.DomainContext), "k", []byte("v"))
	err := storage.Export(context.Background(), db, storage.DomainContext, &failingWriter{})
	if err == nil {
		t.Fatal("Export to a failing writer: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("write-failure error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// discardWriter always succeeds, discarding everything written — used
// where a test needs a valid io.Writer but never inspects its content.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// failingWriter always fails — used to exercise Export's write-error
// branch.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated write failure")
}
