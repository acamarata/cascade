// Purpose: ExportPortable's real-artifact proof — the tar.zst.age byte
//
//	slice is a genuine reference-library round trip (age-decrypt,
//	zstd-decompress, tar-decode all succeed against ExportPortable's own
//	output) and carries exactly the object scope the contract specifies
//	(config, the id chain's manifests, id's OWN objects — never an
//	ancestor's) — plus the elevation and input-validation refusals.
//
// SPORT: internal.backup.export/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// decodeExportedTar is the test-side inverse of ExportPortable's own
// pipeline, used to inspect what actually landed in an artifact without
// going through ImportPortable (whose own correctness is a separate
// assertion in import_test.go).
func decodeExportedTar(t *testing.T, identity string, artifact []byte) []bundleFile {
	t.Helper()
	compressed, err := Decrypt(identity, artifact)
	if err != nil {
		t.Fatalf("Decrypt exported artifact: %v", err)
	}
	tarBytes, err := Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress exported artifact: %v", err)
	}
	bundle, err := decodeImportBundle(tarBytes)
	if err != nil {
		t.Fatalf("decodeImportBundle exported artifact: %v", err)
	}
	return bundle
}

// TestPortableExportRoundTrip is the ticket's end-to-end proof: export ->
// import onto a fresh destination -> the gate -> Restore from the landed
// repo -> asserted domain equality against the real source rows, not
// merely "no error returned."
func TestPortableExportRoundTrip(t *testing.T) {
	source, pub, m, destDB, sourceRows := restoreFixture(t)
	ctx := context.Background()

	artifact, err := ExportPortable(ctx, "export-proof-1", ExportOptions{Target: source}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable: %v", err)
	}

	dest := newMemTarget()
	report, err := ImportPortable(ctx, "import-proof-1", ImportOptions{Dest: dest, PubKey: pub}, artifact, m.Snapshot)
	if err != nil {
		t.Fatalf("ImportPortable: %v", err)
	}
	if report.ManifestsLanded != 1 || report.ObjectsLanded == 0 {
		t.Fatalf("ImportReport = %+v, want exactly 1 manifest and >0 objects landed", report)
	}

	restoreReport, err := Restore(ctx, "restore-proof-1", RestoreOptions{Target: dest, DB: destDB, PubKey: pub}, m.Snapshot)
	if err != nil {
		t.Fatalf("Restore from landed repo: %v", err)
	}
	if restoreReport.RowsImported != len(sourceRows) {
		t.Fatalf("RowsImported = %d, want %d (source row count)", restoreReport.RowsImported, len(sourceRows))
	}
	destRows := readKVRows(t, destDB, "context")
	if len(destRows) != len(sourceRows) {
		t.Fatalf("destination has %d rows, want %d", len(destRows), len(sourceRows))
	}
	for i, src := range sourceRows {
		got := destRows[i]
		if got.namespace != src.namespace || got.key != src.key || string(got.value) != string(src.value) {
			t.Fatalf("destRows[%d] = {%q,%q,%q}, want {%q,%q,%q} (byte-for-byte source match)",
				i, got.namespace, got.key, got.value, src.namespace, src.key, src.value)
		}
	}
}

// TestPortableExportObjectScopeIsTargetSnapshotOnly pins the deliberate
// scope decision export.go's doc comment states: a chained second
// snapshot's export carries only ITS OWN manifest entries' objects, never
// an ancestor's — mirroring VerifyIntegrity's own documented scope
// (integrity.go).
func TestPortableExportObjectScopeIsTargetSnapshotOnly(t *testing.T) {
	ctx := context.Background()
	target, _, _, second := distinctChainedSnapshots(t)
	head := second.Snapshot
	identity := os.Getenv(AgeIdentityEnvVar)
	if identity == "" {
		t.Fatal("distinctChainedSnapshots did not leave an age identity set")
	}

	artifact, err := ExportPortable(ctx, "proof", ExportOptions{Target: target}, head)
	if err != nil {
		t.Fatalf("ExportPortable: %v", err)
	}
	bundle := decodeExportedTar(t, identity, artifact)

	headManifest, err := fetchManifestUnverified(ctx, target, identity, head)
	if err != nil {
		t.Fatalf("fetchManifestUnverified(head): %v", err)
	}
	wantObjects := map[string]bool{}
	for _, e := range headManifest.Entries {
		for _, r := range e.Refs {
			ref, err := manifestRefToObjectRef(r)
			if err != nil {
				t.Fatalf("manifestRefToObjectRef: %v", err)
			}
			wantObjects[ObjectKey(ref.Hash)] = true
		}
	}
	gotObjects := 0
	for _, f := range bundle {
		if hasPrefix(f.Name, repoObjectsDir+"/") {
			gotObjects++
			if !wantObjects[f.Name] {
				t.Fatalf("exported object %s does not belong to head snapshot %s's own manifest entries", f.Name, head)
			}
		}
	}
	if gotObjects != len(wantObjects) {
		t.Fatalf("exported %d objects, want exactly %d (head's own entries only)", gotObjects, len(wantObjects))
	}
	// Both manifests of the chain must ride along so the gate can
	// chain-verify, even though only the head's objects were bundled.
	manifestCount := 0
	for _, f := range bundle {
		if hasPrefix(f.Name, repoManifestsDir+"/") {
			manifestCount++
		}
	}
	if manifestCount != 2 {
		t.Fatalf("exported %d manifests, want 2 (the full chain)", manifestCount)
	}
}

func TestExportRefusesWithoutElevationProof(t *testing.T) {
	target, _, m, _, _ := restoreFixture(t)
	_, err := ExportPortable(context.Background(), "", ExportOptions{Target: target}, m.Snapshot)
	if err != ErrExportElevationRequired {
		t.Fatalf("ExportPortable(no proof) = %v, want ErrExportElevationRequired", err)
	}
}

func TestExportRefusesNilTarget(t *testing.T) {
	_, err := ExportPortable(context.Background(), "proof", ExportOptions{}, "snap-1")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ExportPortable(nil target) = %v, want KindInvalidInput", err)
	}
}

func TestExportRefusesEmptySnapshotID(t *testing.T) {
	target, _, _, _, _ := restoreFixture(t)
	_, err := ExportPortable(context.Background(), "proof", ExportOptions{Target: target}, "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ExportPortable(empty id) = %v, want KindInvalidInput", err)
	}
}

func TestExportPropagatesMissingIdentity(t *testing.T) {
	target, _, m, _, _ := restoreFixture(t)
	t.Setenv(AgeIdentityEnvVar, "")
	_, err := ExportPortable(context.Background(), "proof", ExportOptions{Target: target}, m.Snapshot)
	if err != ErrAgeIdentityMissing {
		t.Fatalf("ExportPortable(no identity) = %v, want ErrAgeIdentityMissing", err)
	}
}
