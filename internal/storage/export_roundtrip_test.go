package storage_test

// Purpose: TestImportRoundTrip — Export -> Import (fresh target) ->
//
//	re-Export byte-for-byte equality, exercised across the awkward
//	payload shapes a "three tidy string keys" test would never catch:
//	an empty value, non-UTF8 binary bytes, a very large value (well past
//	bufio.Scanner's 64KiB default token cap — see export_import.go's
//	readImportLine doc for why that specific trap matters here), a key
//	containing a double quote, a key containing an embedded newline, a
//	key containing multi-byte Unicode, and an entirely empty domain
//	(header only, zero rows). Round count and field values are confirmed
//	via readKVRow's direct SELECT, not merely inferred from ImportReport.
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// roundTripCase names one awkward payload proved by TestImportRoundTrip.
type roundTripCase struct {
	name  string
	key   string
	value []byte
}

func roundTripCases() []roundTripCase {
	large := make([]byte, 2*1024*1024) // 2MiB: past bufio.Scanner's 64KiB default cap.
	for i := range large {
		large[i] = byte(i % 251)
	}
	return []roundTripCase{
		{name: "empty value", key: "empty-value", value: []byte{}},
		{name: "binary non-UTF8 bytes", key: "binary", value: []byte{0x00, 0xFF, 0xC0, 0xC1, 0xFE, 0xFF, 0x80, 0x81}},
		{name: "very large value (2MiB)", key: "large", value: large},
		{name: "key with double quote", key: `has"quote`, value: []byte("v1")},
		{name: "key with embedded newline", key: "has\nnewline", value: []byte("v2")},
		{name: "key with unicode", key: "unicode-é中\U0001F600", value: []byte("v3")},
	}
}

// TestImportRoundTrip proves Export -> Import into a fresh database ->
// re-Export reproduces the original byte-for-byte, across every awkward
// case in roundTripCases, plus a separate empty-domain sub-test.
//
// exportClock is frozen for the WHOLE test (both the source Export and the
// re-Export use the identical instant): ExportedAt is part of the header
// line the byte-for-byte comparison below covers, and Export's own doc
// comment is explicit that two real, unfrozen calls seeing different wall
// time is expected, not a determinism bug — this test needs both calls to
// see the SAME time to isolate what it actually wants to prove (the DATA
// round-trips exactly), and freezing the clock keeps it that way under
// slow (e.g. -race, 2MiB base64) execution instead of flaking whenever the
// two Export calls straddle a wall-clock second.
func TestImportRoundTrip(t *testing.T) {
	defer storage.SetExportClock(testkit.NewFrozenClock(exportTestClock))()
	srcDB := bootstrappedTestDB(t)
	cases := roundTripCases()
	for _, c := range cases {
		seedKVRow(t, srcDB, string(storage.DomainMemory), c.key, c.value)
	}

	var original bytes.Buffer
	if err := storage.Export(context.Background(), srcDB, storage.DomainMemory, &original); err != nil {
		t.Fatalf("Export (source): %v", err)
	}

	dstDB := bootstrappedTestDB(t)
	report, err := storage.Import(context.Background(), dstDB, storage.DomainMemory, bytes.NewReader(original.Bytes()), storage.ImportOpts{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.RowsImported != len(cases) {
		t.Errorf("RowsImported = %d, want %d", report.RowsImported, len(cases))
	}

	// Art.2: direct SELECT after Import, not merely ImportReport's say-so.
	for _, c := range cases {
		got, exists := readKVRow(t, dstDB, string(storage.DomainMemory), c.key)
		if !exists {
			t.Errorf("case %s: key %q missing after Import", c.name, c.key)
			continue
		}
		if !bytes.Equal(got, c.value) {
			t.Errorf("case %s: key %q value mismatch after Import (len got=%d want=%d)", c.name, c.key, len(got), len(c.value))
		}
	}

	var reExported bytes.Buffer
	if err := storage.Export(context.Background(), dstDB, storage.DomainMemory, &reExported); err != nil {
		t.Fatalf("Export (re-export): %v", err)
	}
	if !bytes.Equal(original.Bytes(), reExported.Bytes()) {
		t.Fatalf("re-export does not match original byte-for-byte (len original=%d len re-exported=%d)",
			original.Len(), reExported.Len())
	}
}

// TestImportRoundTrip_EmptyDomain proves the empty-domain case
// separately: Export of a domain with zero kv rows produces a header-only
// stream, and Import of that stream writes zero rows without error.
func TestImportRoundTrip_EmptyDomain(t *testing.T) {
	srcDB := bootstrappedTestDB(t) // never seeded — genuinely empty domain.

	var exported bytes.Buffer
	if err := storage.Export(context.Background(), srcDB, storage.DomainAudit, &exported); err != nil {
		t.Fatalf("Export (empty domain): %v", err)
	}
	// Exactly one line: the header.
	if n := bytes.Count(exported.Bytes(), []byte("\n")); n != 1 {
		t.Errorf("empty-domain export has %d newlines, want 1 (header only)", n)
	}

	dstDB := bootstrappedTestDB(t)
	report, err := storage.Import(context.Background(), dstDB, storage.DomainAudit, bytes.NewReader(exported.Bytes()), storage.ImportOpts{})
	if err != nil {
		t.Fatalf("Import (empty domain): %v", err)
	}
	if report.RowsImported != 0 || report.RowsSkipped != 0 || report.RowsOverwritten != 0 {
		t.Errorf("empty-domain Import report = %+v, want all zero", report)
	}
	if n := countKVRows(t, dstDB, string(storage.DomainAudit)); n != 0 {
		t.Errorf("empty-domain Import wrote %d rows, want 0", n)
	}
}
