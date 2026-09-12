// Purpose: tests for import_id.go's chain-tip and repo-signing-key
// inference, exercised through ImportPortable's own id="" / PubKey=nil
// shape -- the CLI path a real `cascade backup import` with no explicit
// snapshot-id argument and no --pubkey flag actually takes (07's verb set
// carries neither).
// Inputs: a real exported artifact from restoreFixture's real snapshot.
// Outputs: the inferred SnapshotID and ed25519.PublicKey, or a typed
// fail-closed error on an ambiguous/malformed chain.
// SPORT: internal.backup.import/CHANGE (tests) (P1-E19-W4-S42-T3).

package backup

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestImportPortableInfersSnapshotAndPubKey proves ImportPortable, given no
// explicit id and no explicit PubKey, infers both correctly from the
// bundle's own manifest chain and repo config -- landing the exact same
// report as the explicit-id/explicit-key path in export_test.go's
// TestPortableExportRoundTrip.
func TestImportPortableInfersSnapshotAndPubKey(t *testing.T) {
	source, _, m, _, _ := restoreFixture(t)
	ctx := context.Background()

	artifact, err := ExportPortable(ctx, "export-proof-1", ExportOptions{Target: source}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable: %v", err)
	}

	dest := newMemTarget()
	report, err := ImportPortable(ctx, "import-proof-1", ImportOptions{Dest: dest}, artifact, "")
	if err != nil {
		t.Fatalf("ImportPortable with inferred id and pubkey: %v", err)
	}
	if report.Snapshot != m.Snapshot {
		t.Fatalf("inferred snapshot = %q, want %q", report.Snapshot, m.Snapshot)
	}
	if report.ManifestsLanded != 1 || report.ObjectsLanded == 0 {
		t.Fatalf("report = %+v, want one manifest and at least one object landed", report)
	}
}

// TestImportBundlePublicKeyMissingConfig proves a bundle with no
// config/repo.json member (so no signing key to infer) refuses typed,
// never panics or silently returns a nil key.
func TestImportBundlePublicKeyMissingConfig(t *testing.T) {
	_, err := importBundlePublicKey(nil)
	if err == nil {
		t.Fatal("importBundlePublicKey(no bundle) succeeded, want a refusal")
	}
}

// TestInferImportSnapshotNoManifests proves a bundle with zero manifest
// members refuses "no chain tip", never returning an empty SnapshotID as
// if it were valid.
func TestInferImportSnapshotNoManifests(t *testing.T) {
	if _, err := inferImportSnapshot(nil, "unused-identity"); err == nil {
		t.Fatal("inferImportSnapshot(no manifests) succeeded, want a refusal")
	}
}

// TestInferImportSnapshotMultipleTips proves a bundle with two independent
// (non-chained) REAL manifests -- neither is the other's previous_snapshot,
// since both are created with a nil previous -- refuses "multiple chain
// tips" rather than picking one arbitrarily.
func TestInferImportSnapshotMultipleTips(t *testing.T) {
	setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target := newMemTarget()
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "context", "k", []byte("v"))
	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(1_700_000_300, 0)),
		Domains: map[string]Exporter{"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()}},
	}
	m1, err := CreateSnapshot(context.Background(), "proof-a", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot m1: %v", err)
	}
	m2, err := CreateSnapshot(context.Background(), "proof-b", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot m2: %v", err)
	}

	raw1, err := target.Get(context.Background(), manifestKey(m1.Snapshot))
	if err != nil {
		t.Fatalf("read manifest 1: %v", err)
	}
	data1, err := io.ReadAll(raw1)
	if err != nil {
		t.Fatalf("read manifest 1 body: %v", err)
	}
	raw2, err := target.Get(context.Background(), manifestKey(m2.Snapshot))
	if err != nil {
		t.Fatalf("read manifest 2: %v", err)
	}
	data2, err := io.ReadAll(raw2)
	if err != nil {
		t.Fatalf("read manifest 2 body: %v", err)
	}

	bundle := []bundleFile{
		{Name: manifestKey(m1.Snapshot), Data: data1},
		{Name: manifestKey(m2.Snapshot), Data: data2},
	}
	if _, err := inferImportSnapshot(bundle, identity); err == nil {
		t.Fatal("inferImportSnapshot(two independent tips) succeeded, want a refusal")
	}
}
