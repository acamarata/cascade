package storage_test

// Purpose: TestImportAtomicRollback — a corrupt JSON record mid-stream
//
//	rolls back the entire write transaction: zero rows written, domain
//	state identical before and after, verified by row count AND
//	re-export equality (acceptance criteria "Atomic rollback").
//
// SPORT: internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestImportAtomicRollback seeds the target domain with one pre-existing
// row (so "domain state identical before and after" is a real, non-
// vacuous assertion), then imports a stream whose second row line is
// truncated/corrupt JSON. Rows before the corrupt line must NOT survive
// either — the whole transaction rolls back, not just the failing line.
func TestImportAtomicRollback(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedKVRow(t, db, string(storage.DomainSessions), "preexisting", []byte("untouched"))

	var before bytes.Buffer
	if err := storage.Export(context.Background(), db, storage.DomainSessions, &before); err != nil {
		t.Fatalf("Export (before): %v", err)
	}

	// Two well-formed rows followed by a truncated/corrupt third line.
	stream := strings.NewReader(
		`{"_type":"header","domain":"sessions","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"good1","value":"dg=="}` + "\n" +
			`{"_type":"row","key":"good2","value":"dg=="}` + "\n" +
			`{"_type":"row","key":"corrupt` + "\n", // truncated: missing closing quote/brace
	)

	_, err := storage.Import(context.Background(), db, storage.DomainSessions, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import with corrupt mid-stream record: want an error, got nil")
	}

	for _, key := range []string{"good1", "good2", "corrupt"} {
		if _, exists := readKVRow(t, db, string(storage.DomainSessions), key); exists {
			t.Errorf("key %q was written despite the mid-stream rollback", key)
		}
	}

	if n := countKVRows(t, db, string(storage.DomainSessions)); n != 1 {
		t.Errorf("row count after failed import = %d, want 1 (only the pre-existing row)", n)
	}

	var after bytes.Buffer
	if err := storage.Export(context.Background(), db, storage.DomainSessions, &after); err != nil {
		t.Fatalf("Export (after): %v", err)
	}
	if !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatalf("domain state changed across the failed import:\n--- before ---\n%s\n--- after ---\n%s", before.Bytes(), after.Bytes())
	}
}

// TestImportAtomicRollback_BadBase64Value proves the same rollback
// guarantee for a structurally-valid-JSON row whose "value" field is not
// valid base64 — a different corruption shape from a truncated line.
func TestImportAtomicRollback_BadBase64Value(t *testing.T) {
	db := bootstrappedTestDB(t)
	stream := strings.NewReader(
		`{"_type":"header","domain":"blobs","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n" +
			`{"_type":"row","key":"good","value":"dg=="}` + "\n" +
			`{"_type":"row","key":"bad","value":"not-valid-base64!!"}` + "\n",
	)
	_, err := storage.Import(context.Background(), db, storage.DomainBlobs, stream, storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import with invalid base64 value: want an error, got nil")
	}
	if n := countKVRows(t, db, string(storage.DomainBlobs)); n != 0 {
		t.Errorf("row count after bad-base64 import = %d, want 0", n)
	}
}
