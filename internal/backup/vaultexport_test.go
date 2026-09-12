// Purpose: §D-34's central provable claim — the default export carries NO
//
//	vault material — plus the opt-in leg's real (interface-boundary) wiring:
//	a fake VaultExporter/VaultImporter standing in for the not-yet-landed
//	Epic H Broker.Export/Import (see vaultexport.go's Purpose header for
//	why), proving addVaultMember/importVaultMember actually call through
//	and never see or persist an unwrapped value.
//
// SPORT: internal.backup.vaultexport/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeVaultBroker stands in for the not-yet-landed Epic H
// Broker.Export/Import pair. It records the passphrase it was called
// with (test-only visibility into what crossed the interface boundary)
// and returns/accepts a fixed "already wrapped" envelope — this package
// never inspects or produces that envelope's actual bytes itself.
type fakeVaultBroker struct {
	exportCalls      int
	importCalls      int
	lastExportedWith string
	lastImportedWith string
	lastImportedData []byte
	exportErr        error
	importErr        error
}

func (f *fakeVaultBroker) Export(_ context.Context, req VaultExportRequest) ([]byte, error) {
	f.exportCalls++
	f.lastExportedWith = req.Passphrase
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	return []byte("fake-wrapped-vault-envelope"), nil
}

func (f *fakeVaultBroker) Import(_ context.Context, req VaultImportRequest) error {
	f.importCalls++
	f.lastImportedWith = req.Passphrase
	f.lastImportedData = req.Wrapped
	return f.importErr
}

// TestVaultExportOptInDefaultOff is the ticket's central provable claim:
// the zero-value ExportOptions (IncludeVault false, no broker, no
// passphrase) NEVER emits a vault member, verified by decrypting and
// decoding the real artifact bytes rather than inspecting ExportOptions
// itself.
func TestVaultExportOptInDefaultOff(t *testing.T) {
	source, _, m, _, _ := restoreFixture(t)
	ctx := context.Background()

	artifact, err := ExportPortable(ctx, "proof", ExportOptions{Target: source}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable (default options): %v", err)
	}
	identity := mustGetenv(t, AgeIdentityEnvVar)
	bundle := decodeExportedTar(t, identity, artifact)
	if hasBundleMember(bundle, vaultBundleName) {
		t.Fatal("default ExportOptions produced a vault member; §D-34 requires opt-in, off by default")
	}
}

// TestVaultExportOptInProducesWrappedEnvelope proves the opt-in path is
// real, not merely a flag: with IncludeVault + a passphrase + a broker,
// the resulting artifact carries a vault member holding EXACTLY the
// broker's returned envelope, and the broker was called with the supplied
// passphrase — never a raw secret, never this package's own encoding.
func TestVaultExportOptInProducesWrappedEnvelope(t *testing.T) {
	source, pub, m, _, _ := restoreFixture(t)
	ctx := context.Background()
	broker := &fakeVaultBroker{}

	artifact, err := ExportPortable(ctx, "proof", ExportOptions{
		Target: source, IncludeVault: true, VaultPassphrase: "correct horse battery staple", VaultBroker: broker,
	}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable (opt-in): %v", err)
	}
	if broker.exportCalls != 1 || broker.lastExportedWith != "correct horse battery staple" {
		t.Fatalf("broker.Export called %d time(s) with %q, want exactly 1 with the supplied passphrase",
			broker.exportCalls, broker.lastExportedWith)
	}
	identity := mustGetenv(t, AgeIdentityEnvVar)
	bundle := decodeExportedTar(t, identity, artifact)
	data, ok := findBundleMember(bundle, vaultBundleName)
	if !ok {
		t.Fatal("opt-in ExportOptions did not produce a vault member")
	}
	if !bytes.Equal(data, []byte("fake-wrapped-vault-envelope")) {
		t.Fatalf("vault member = %q, want the broker's exact returned envelope", data)
	}

	// Restore-side import: ImportPortable should call VaultImporter.Import
	// with that same envelope and the supplied passphrase, and report
	// VaultImported true.
	dest := newMemTarget()
	report, err := ImportPortable(ctx, "import-proof", ImportOptions{
		Dest: dest, PubKey: pub, VaultImporter: broker, VaultPassphrase: "correct horse battery staple",
	}, artifact, m.Snapshot)
	if err != nil {
		t.Fatalf("ImportPortable: %v", err)
	}
	if !report.VaultImported {
		t.Fatal("ImportReport.VaultImported = false, want true")
	}
	if broker.importCalls != 1 || broker.lastImportedWith != "correct horse battery staple" {
		t.Fatalf("broker.Import called %d time(s) with %q, want exactly 1 with the supplied passphrase",
			broker.importCalls, broker.lastImportedWith)
	}
	if !bytes.Equal(broker.lastImportedData, []byte("fake-wrapped-vault-envelope")) {
		t.Fatalf("broker.Import received %q, want the exported envelope unchanged", broker.lastImportedData)
	}
}

func TestExportOptInRefusesWithoutPassphrase(t *testing.T) {
	source, _, m, _, _ := restoreFixture(t)
	_, err := ExportPortable(context.Background(), "proof",
		ExportOptions{Target: source, IncludeVault: true, VaultBroker: &fakeVaultBroker{}}, m.Snapshot)
	if err != ErrVaultExportPassphraseRequired {
		t.Fatalf("ExportPortable(opt-in, no passphrase) = %v, want ErrVaultExportPassphraseRequired", err)
	}
}

func TestExportOptInRefusesWithoutBroker(t *testing.T) {
	source, _, m, _, _ := restoreFixture(t)
	_, err := ExportPortable(context.Background(), "proof",
		ExportOptions{Target: source, IncludeVault: true, VaultPassphrase: "x"}, m.Snapshot)
	if err != ErrVaultExportBrokerRequired {
		t.Fatalf("ExportPortable(opt-in, no broker) = %v, want ErrVaultExportBrokerRequired", err)
	}
}

// TestImportRefusesPresentVaultMemberWithoutPassphrase proves the restore
// side never silently skips a vault export that IS present just because
// the caller forgot the passphrase — §D-34's "required at import".
func TestImportRefusesPresentVaultMemberWithoutPassphrase(t *testing.T) {
	source, pub, m, _, _ := restoreFixture(t)
	ctx := context.Background()
	broker := &fakeVaultBroker{}
	artifact, err := ExportPortable(ctx, "proof", ExportOptions{
		Target: source, IncludeVault: true, VaultPassphrase: "secret", VaultBroker: broker,
	}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable: %v", err)
	}
	dest := newMemTarget()
	_, err = ImportPortable(ctx, "proof", ImportOptions{Dest: dest, PubKey: pub}, artifact, m.Snapshot)
	if err != ErrVaultImportPassphraseRequired {
		t.Fatalf("ImportPortable(vault present, no passphrase) = %v, want ErrVaultImportPassphraseRequired", err)
	}
}

func TestVaultBrokerExportErrorPropagates(t *testing.T) {
	source, _, m, _, _ := restoreFixture(t)
	broker := &fakeVaultBroker{exportErr: cascade.New(cascade.KindUnavailable, "vault backend offline")}
	_, err := ExportPortable(context.Background(), "proof",
		ExportOptions{Target: source, IncludeVault: true, VaultPassphrase: "x", VaultBroker: broker}, m.Snapshot)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ExportPortable(broker error) = %v, want KindUnavailable propagated", err)
	}
}

func mustGetenv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set in the test environment", name)
	}
	return v
}
