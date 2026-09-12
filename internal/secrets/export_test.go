// Purpose: tests for elevated backup export/import surface.
// Inputs: in-memory custody, allow/deny elevation gates.
// Outputs: verified round-trip, refusal, and zeroing behaviour.
// Constraints: no secret value appears in error messages; zero() is verified
// on its own; authorization is always tested before custody access.
// SPORT: internal.secrets.Broker.Export/ADD, Broker.Import/ADD (P1-E19-W4-S42-T3).
package secrets

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestExportRequiresPassphrase(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	_, err := b.Export(context.Background(), ExportRequest{})
	if !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Export without passphrase = %v, want KindInvalidInput", err)
	}
}

func TestExportRequiresElevation(t *testing.T) {
	b, _ := newTestBroker(t, denyGate{})
	_, err := b.Export(context.Background(), ExportRequest{Passphrase: "p"})
	if !isKind(err, cascade.KindElevationRequired) {
		t.Fatalf("Export without elevation = %v, want KindElevationRequired", err)
	}
}

func TestImportRequiresElevation(t *testing.T) {
	b, _ := newTestBroker(t, denyGate{})
	err := b.Import(context.Background(), ImportRequest{Passphrase: "p", Wrapped: []byte("x")})
	if !isKind(err, cascade.KindElevationRequired) {
		t.Fatalf("Import without elevation = %v, want KindElevationRequired", err)
	}
}

func TestImportRequiresInputs(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	ctx := context.Background()
	err := b.Import(ctx, ImportRequest{Wrapped: []byte("x")})
	if !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Import without passphrase = %v, want KindInvalidInput", err)
	}
	err = b.Import(ctx, ImportRequest{Passphrase: "p"})
	if !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Import without envelope = %v, want KindInvalidInput", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	ctx := context.Background()
	custody.entries["TOKEN-A"] = []byte("secret-a")
	custody.entries["TOKEN-B"] = []byte("secret-b")

	wrapped, err := b.Export(ctx, ExportRequest{Passphrase: "hunter2"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(wrapped) == 0 {
		t.Fatal("Export returned empty bytes")
	}
	b2, custody2 := newTestBroker(t, &allowGate{})
	if err := b2.Import(ctx, ImportRequest{Wrapped: wrapped, Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for _, name := range []string{"TOKEN-A", "TOKEN-B"} {
		if _, ok := custody2.entries[name]; !ok {
			t.Errorf("Import: missing entry %q", name)
		}
	}
}

func TestImportWrongPassphrase(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	custody.entries["TOKEN"] = []byte("s3cr3t")
	wrapped, err := b.Export(context.Background(), ExportRequest{Passphrase: "correct"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	b2, _ := newTestBroker(t, &allowGate{})
	err = b2.Import(context.Background(), ImportRequest{Wrapped: wrapped, Passphrase: "wrong"})
	if !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("wrong passphrase = %v, want KindIntegrity", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatal("secret value leaked into error")
	}
}

func TestImportGarbage(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	err := b.Import(context.Background(), ImportRequest{Wrapped: []byte("not-age-ciphertext"), Passphrase: "p"})
	if !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("garbage ciphertext = %v, want KindIntegrity", err)
	}
}

func TestDecodeVaultExportUnknownVersion(t *testing.T) {
	_, err := decodeVaultExport([]byte(`{"version":99,"entries":[]}`))
	if !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("unknown version = %v, want KindUnsupported", err)
	}
}

func TestDecodeVaultExportTrailingData(t *testing.T) {
	_, err := decodeVaultExport([]byte(`{"version":1,"entries":[]}{}`))
	if !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("trailing data = %v, want KindIntegrity", err)
	}
}

func TestDecodeVaultExportInvalidName(t *testing.T) {
	_, err := decodeVaultExport([]byte(`{"version":1,"entries":[{"name":"bad/name","value":""}]}`))
	if err == nil {
		t.Fatal("invalid entry name was accepted")
	}
}

func TestZeroByteSlice(t *testing.T) {
	b := []byte{1, 2, 3}
	zero(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("zero: b[%d] = %d, want 0", i, v)
		}
	}
}

func TestZeroExportDocumentEntries(t *testing.T) {
	doc := vaultExportDocument{
		Entries: []vaultExportRecord{
			{Name: "x", Value: []byte{1, 2, 3}},
		},
	}
	zeroExportDocument(doc)
	for i, v := range doc.Entries[0].Value {
		if v != 0 {
			t.Errorf("zeroExportDocument: entries[0].Value[%d] = %d, want 0", i, v)
		}
	}
}

// TestExportVerbsAuthorizedCorrectly verifies that Export and Import gate on
// VerbBackupExport and VerbBackupImport respectively — not on any other verb.
func TestExportVerbsAuthorizedCorrectly(t *testing.T) {
	gate := &allowGate{}
	b, _ := newTestBroker(t, gate)
	b.Export(context.Background(), ExportRequest{Passphrase: "p"}) //nolint:errcheck
	for _, v := range gate.seen {
		if v == VerbBackupExport {
			goto foundExport
		}
	}
	t.Fatal("Export did not authorize VerbBackupExport")
foundExport:

	gate2 := &allowGate{}
	b2, _ := newTestBroker(t, gate2)
	b2.Import(context.Background(), ImportRequest{Passphrase: "p", Wrapped: []byte("x")}) //nolint:errcheck
	for _, v := range gate2.seen {
		if v == VerbBackupImport {
			return
		}
	}
	t.Fatal("Import did not authorize VerbBackupImport")
}
