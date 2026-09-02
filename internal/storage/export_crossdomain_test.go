package storage_test

// Purpose: cross-domain and reserved-namespace safety — importing a file
//
//	exported from domain A into domain B is refused (never silently
//	remapped), and R-14.100's reserved PluginStorage namespace is never a
//	valid Import target, either as the target argument or as the
//	exported-from domain a file claims.
//
// SPORT: internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestImportCrossDomainRefusal proves a file whose header names domain A
// is refused when the caller asks to import it into domain B — never
// remapped, per export_import.go's design decision 3.
func TestImportCrossDomainRefusal(t *testing.T) {
	srcDB := bootstrappedTestDB(t)
	seedKVRow(t, srcDB, string(storage.DomainSecrets), "k", []byte("v"))

	var exported strings.Builder
	if err := storage.Export(context.Background(), srcDB, storage.DomainSecrets, &exported); err != nil {
		t.Fatalf("Export (domain A = secrets): %v", err)
	}

	dstDB := bootstrappedTestDB(t)
	report, err := storage.Import(context.Background(), dstDB, storage.DomainAudit, // domain B
		strings.NewReader(exported.String()), storage.ImportOpts{})
	if err == nil {
		t.Fatal("Import of a secrets-domain export into audit: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("cross-domain refusal error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	if report != (storage.ImportReport{}) {
		t.Errorf("ImportReport = %+v, want zero value", report)
	}
	if n := countKVRows(t, dstDB, string(storage.DomainAudit)); n != 0 {
		t.Errorf("cross-domain refusal wrote %d rows into the target domain, want 0", n)
	}
	// The source domain's own data is of course untouched by this refused
	// import into a different database entirely — sanity-checked so a
	// future refactor cannot accidentally point Import at srcDB.
	if _, exists := readKVRow(t, srcDB, string(storage.DomainSecrets), "k"); !exists {
		t.Error("source domain row disappeared, which this test never asked to happen")
	}
}

// TestImportReservedNamespaceRefusal proves R-14.100's reserved
// PluginStorage namespace ("plugin.__host__") is refused as an Import
// target: it is not a member of the closed AllDomains set, so validDomain
// (the same check capability.go's Grant/Check already enforce) rejects it
// before the reader is even touched.
func TestImportReservedNamespaceRefusal(t *testing.T) {
	db := bootstrappedTestDB(t)
	reserved := storage.DomainID(storage.ReservedPluginHostNamespace)

	report, err := storage.Import(context.Background(), db, reserved,
		strings.NewReader(`{"_type":"header","domain":"plugin.__host__","schema_version":1,"exported_at":"2026-09-01T12:00:00Z","cascade_version":"dev"}`+"\n"),
		storage.ImportOpts{},
	)
	if err == nil {
		t.Fatal("Import into the reserved plugin.__host__ namespace: want an error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("reserved-namespace refusal error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	if report != (storage.ImportReport{}) {
		t.Errorf("ImportReport = %+v, want zero value", report)
	}
}

// TestExportReservedNamespaceRefusal proves the identical refusal on the
// Export side: the reserved namespace is not exportable either.
func TestExportReservedNamespaceRefusal(t *testing.T) {
	db := bootstrappedTestDB(t)
	reserved := storage.DomainID(storage.ReservedPluginHostNamespace)
	err := storage.Export(context.Background(), db, reserved, &strings.Builder{})
	if err == nil {
		t.Fatal("Export of the reserved plugin.__host__ namespace: want an error, got nil")
	}
	if !errors.Is(err, cascade.ErrInvalidInput) {
		t.Errorf("Export reserved-namespace error = %v, want KindInvalidInput", err)
	}
}
