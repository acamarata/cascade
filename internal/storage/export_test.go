package storage_test

// Purpose: TestExportDeterminism, TestExportGolden, and FuzzImportRecord —
//
//	the determinism contract's two positive proofs plus the ticket's
//	Art.2 fuzz target (06-FORGE-SPEC.md §5.7: any ticket adding a
//	parser/decoder ships a FuzzXxx target). FuzzImportRecord lives here
//	specifically (not split to a sibling) because the ticket's task text
//	names export_test.go as its home verbatim.
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// canonicalGoldenDomain and canonicalGoldenRows are the fixed domain state
// TestExportGolden captures — see testdata/T3-PROVENANCE.md (this
// ticket's substitute for testdata/README.md; see that file's own note
// for why) for the exact capture command and tool/version/date
// provenance.
var canonicalGoldenDomain = storage.DomainContext

var canonicalGoldenRows = []struct {
	Key   string
	Value []byte
}{
	{Key: "alpha", Value: []byte("hello world")},
	{Key: "beta", Value: []byte("")},
	{Key: "gamma", Value: []byte{0x00, 0x01, 0xFE, 0xFF}},
}

// seedCanonicalGoldenDomain seeds db with canonicalGoldenRows under
// canonicalGoldenDomain, used by both TestExportDeterminism and
// TestExportGolden so the two tests exercise the identical fixed state.
func seedCanonicalGoldenDomain(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, row := range canonicalGoldenRows {
		seedKVRow(t, db, string(canonicalGoldenDomain), row.Key, row.Value)
	}
}

// TestExportDeterminism proves Export called twice on identical database
// state (same rows, same frozen exportClock reading) produces byte-for-
// byte identical output — the determinism contract's core claim,
// independent of the golden fixture.
func TestExportDeterminism(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedCanonicalGoldenDomain(t, db)
	defer storage.SetExportClock(testkit.NewFrozenClock(exportTestClock))()

	var first, second bytes.Buffer
	if err := storage.Export(context.Background(), db, canonicalGoldenDomain, &first); err != nil {
		t.Fatalf("Export (first): %v", err)
	}
	if err := storage.Export(context.Background(), db, canonicalGoldenDomain, &second); err != nil {
		t.Fatalf("Export (second): %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("Export is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first.Bytes(), second.Bytes())
	}
}

// TestExportGolden captures Export's output for canonicalGoldenRows under
// a frozen clock and compares it byte-for-byte against the committed
// golden at testdata/golden/export-seed.jsonl (Art.2: the golden was
// itself captured from a real Export run — see
// testdata/T3-PROVENANCE.md — never hand-authored).
func TestExportGolden(t *testing.T) {
	db := bootstrappedTestDB(t)
	seedCanonicalGoldenDomain(t, db)
	defer storage.SetExportClock(testkit.NewFrozenClock(exportTestClock))()

	var buf bytes.Buffer
	if err := storage.Export(context.Background(), db, canonicalGoldenDomain, &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	compareOrUpdateExportGolden(t, buf.Bytes())
}

// FuzzImportRecord fuzzes storage.Import's per-line JSON decoder (both the
// header line and row line shapes) with one raw line at a time, asserting
// only that it never panics — Import returning an error for malformed
// input is the correct, expected outcome and is not a fuzz failure.
// Seeded from internal/testdata/fuzz/FuzzImportRecord/ (06-FORGE-SPEC.md
// §5.7) plus a handful of f.Add seeds covering the shapes this ticket's
// own round-trip/error-path tests already exercise, so `go test -fuzz`
// starts from known-interesting inputs rather than only the corpus file.
func FuzzImportRecord(f *testing.F) {
	seedDir := "../testdata/fuzz/FuzzImportRecord"
	if entries, err := os.ReadDir(seedDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if data, rerr := os.ReadFile(seedDir + "/" + e.Name()); rerr == nil {
				f.Add(data)
			}
		}
	}

	f.Add([]byte(`{"_type":"header","domain":"context","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}`))
	f.Add([]byte(`{"_type":"row","key":"alpha","value":"aGVsbG8="}`))
	f.Add([]byte(`{"_type":"row","key":"","value":""}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(`{"_type":"row","key":"k","value":"not-valid-base64!!"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"_type":"row"`))

	f.Fuzz(func(t *testing.T, line []byte) {
		db := bootstrappedTestDB(t)
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Import panicked on line %q: %v", line, r)
			}
		}()
		// One line, no trailing newline needed — Import's line reader
		// tolerates EOF-without-newline (readImportLine's own contract).
		var stream bytes.Buffer
		stream.WriteString(`{"_type":"header","domain":"context","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}` + "\n")
		stream.Write(line)
		_, _ = storage.Import(context.Background(), db, storage.DomainContext, &stream, storage.ImportOpts{})
	})
}
