// Purpose: ImportPortable's fail-closed proofs — a tampered artifact, a
//
//	wrong identity, and a gate failure all refuse WITHOUT landing content
//	that survives the call (the destination is left exactly as it was
//	found), plus decodeImportBundle's own traversal/size/malformed-input
//	refusals.
//
// SPORT: internal.backup.import/ADDED (P1-E19-W4-S42-T2).

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestImportRefusesWithoutElevationProof(t *testing.T) {
	dest := newMemTarget()
	_, err := ImportPortable(context.Background(), "", ImportOptions{Dest: dest}, []byte("x"), "snap-1")
	if err != ErrImportElevationRequired {
		t.Fatalf("ImportPortable(no proof) = %v, want ErrImportElevationRequired", err)
	}
}

func TestImportRefusesNilDest(t *testing.T) {
	_, err := ImportPortable(context.Background(), "proof", ImportOptions{}, []byte("x"), "snap-1")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ImportPortable(nil dest) = %v, want KindInvalidInput", err)
	}
}

// TestImportGateRefusal proves the ticket's central ordering guarantee: a
// TAMPERED artifact (one bit flipped in an exported object) decrypts fine
// (the outer age wrap is untouched) but fails the gate once landed, and
// the destination is left with ZERO keys afterward — never a
// half-imported repo.
func TestImportGateRefusal(t *testing.T) {
	source, pub, m, _, _ := restoreFixture(t)
	ctx := context.Background()
	artifact, err := ExportPortable(ctx, "export-proof", ExportOptions{Target: source}, m.Snapshot)
	if err != nil {
		t.Fatalf("ExportPortable: %v", err)
	}

	tampered := tamperExportedObject(t, artifact)
	dest := newMemTarget()
	_, err = ImportPortable(ctx, "import-proof", ImportOptions{Dest: dest, PubKey: pub}, tampered, m.Snapshot)
	if err == nil {
		t.Fatal("ImportPortable(tampered artifact) = nil error, want a gate refusal")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("ImportPortable(tampered artifact) kind = %v, want KindIntegrity", err)
	}
	assertTargetEmpty(t, dest)
}

// tamperExportedObject decrypts+decompresses+decodes artifact, flips one
// byte inside its single stored object's ciphertext (never inside the
// manifest, whose own signature check would catch a manifest-level
// tamper first — this test isolates the OBJECT-hash-mismatch refusal
// path specifically), and re-encodes/re-compresses/re-encrypts it back
// into a valid outer-wrap artifact so the tamper is caught by the gate,
// not by the outer AEAD.
func tamperExportedObject(t *testing.T, artifact []byte) []byte {
	t.Helper()
	// The fixture's identity lives in the process env for the duration of
	// the calling test (restoreFixture sets it via t.Setenv); read it back
	// directly rather than threading it through every helper signature.
	identity := os.Getenv(AgeIdentityEnvVar)
	if identity == "" {
		t.Fatal("restoreFixture did not leave an age identity set")
	}
	compressed, err := Decrypt(identity, artifact)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	tarBytes, err := Decompress(compressed)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	bundle, err := decodeImportBundle(tarBytes)
	if err != nil {
		t.Fatalf("decodeImportBundle: %v", err)
	}
	tampered := false
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, f := range bundle {
		data := f.Data
		if !tampered && hasPrefix(f.Name, repoObjectsDir+"/") {
			data = append([]byte{}, data...)
			data[len(data)/2] ^= 0xFF
			tampered = true
		}
		if err := writeTarMember(w, f.Name, data); err != nil {
			t.Fatalf("writeTarMember: %v", err)
		}
	}
	if !tampered {
		t.Fatal("fixture produced no object entry to tamper")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	recompressed, err := Compress(buf.Bytes())
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	// Re-encrypt to the same recipient the original artifact used: derive
	// it from the untampered config member rather than re-reading Target
	// (Target is not passed to this helper by design — it operates purely
	// on the artifact bytes, matching what an attacker who only has the
	// artifact could do).
	recipient := recipientFromBundle(t, bundle)
	reencrypted, err := Encrypt(recipient, recompressed)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return reencrypted
}

// recipientFromBundle decodes the bundle's config/repo.json member to
// recover the age recipient it was originally encrypted to.
func recipientFromBundle(t *testing.T, bundle []bundleFile) string {
	t.Helper()
	data, ok := findBundleMember(bundle, repoConfigKey)
	if !ok {
		t.Fatal("bundle carries no config/repo.json member")
	}
	cfg, err := DecodeRepoConfig(data)
	if err != nil {
		t.Fatalf("DecodeRepoConfig: %v", err)
	}
	return cfg.AgeRecipient
}

// assertTargetEmpty confirms dest holds no key under any of the three
// layout prefixes — the "destination left exactly as it was found"
// guarantee after a refused import.
func assertTargetEmpty(t *testing.T, dest Target) {
	t.Helper()
	ctx := context.Background()
	for _, prefix := range []string{repoConfigDir + "/", repoManifestsDir + "/", repoObjectsDir + "/"} {
		keys, err := dest.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		if len(keys) != 0 {
			t.Fatalf("dest still holds %d key(s) under %s after a refused import: %v", len(keys), prefix, keys)
		}
	}
}

func TestImportPropagatesMissingIdentity(t *testing.T) {
	dest := newMemTarget()
	t.Setenv(AgeIdentityEnvVar, "")
	_, err := ImportPortable(context.Background(), "proof", ImportOptions{Dest: dest}, []byte("x"), "snap-1")
	if err != ErrAgeIdentityMissing {
		t.Fatalf("ImportPortable(no identity) = %v, want ErrAgeIdentityMissing", err)
	}
}

func TestImportRefusesGarbageArtifact(t *testing.T) {
	dest := newMemTarget()
	identity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	_, err := ImportPortable(context.Background(), "proof", ImportOptions{Dest: dest}, []byte("not an age file"), "snap-1")
	if err == nil {
		t.Fatal("ImportPortable(garbage artifact) = nil error, want a decrypt failure")
	}
}

func TestDecodeImportBundleRefusesTraversal(t *testing.T) {
	tarBytes := buildRawTar(t, map[string][]byte{"../etc/passwd": []byte("x")})
	_, err := decodeImportBundle(tarBytes)
	if err != ErrImportBundleTraversal {
		t.Fatalf("decodeImportBundle(traversal) = %v, want ErrImportBundleTraversal", err)
	}
}

func TestDecodeImportBundleRefusesUnknownPrefix(t *testing.T) {
	tarBytes := buildRawTar(t, map[string][]byte{"etc/passwd": []byte("x")})
	_, err := decodeImportBundle(tarBytes)
	if err != ErrImportBundleTraversal {
		t.Fatalf("decodeImportBundle(unknown prefix) = %v, want ErrImportBundleTraversal", err)
	}
}

func TestDecodeImportBundleRefusesMalformed(t *testing.T) {
	_, err := decodeImportBundle([]byte("this is not a tar stream at all, just noise bytes"))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeImportBundle(malformed) = %v, want KindIntegrity", err)
	}
}

func TestDecodeImportBundleNeverPanicsOnEmptyInput(t *testing.T) {
	bundle, err := decodeImportBundle(nil)
	if err != nil {
		t.Fatalf("decodeImportBundle(nil) = %v, want nil (an empty tar is valid, just empty)", err)
	}
	if len(bundle) != 0 {
		t.Fatalf("decodeImportBundle(nil) = %d entries, want 0", len(bundle))
	}
}

// FuzzImportBundleDecode drives decodeImportBundle directly against
// arbitrary bytes (06 §5.7: this ticket's own decoder of an
// externally-carried format) — never through age/zstd, since a fuzzer
// mutating outer-wrap ciphertext only ever exercises AEAD authentication
// failure, never the tar/layout decoder this function actually is. The
// only property under test is "never panics, always returns a typed
// error or a valid []bundleFile" — decodeImportBundle's own doc comment.
func FuzzImportBundleDecode(f *testing.F) {
	f.Add(rawTarBytes(map[string][]byte{"config/repo.json": []byte(`{"layout_version":1}`)}))
	f.Add([]byte(""))
	f.Add([]byte("not a tar stream"))
	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, err := decodeImportBundle(data)
		if err != nil {
			return
		}
		for _, entry := range bundle {
			if !validBundleName(entry.Name) {
				t.Fatalf("decodeImportBundle accepted invalid entry name %q with no error", entry.Name)
			}
		}
	})
}

// rawTarBytes builds a tar byte stream with exactly the given
// name->content entries, with no *testing.T dependency so both the fuzz
// seed corpus (via *testing.F) and ordinary tests (via buildRawTar below)
// can share it.
func rawTarBytes(entries map[string][]byte) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, data := range entries {
		_ = writeTarMember(w, name, data)
	}
	_ = w.Close()
	return buf.Bytes()
}

// buildRawTar is rawTarBytes with test-visible failure on a write error,
// for decoder-level tests that need to construct an invalid bundle no
// real ExportPortable call would ever produce.
func buildRawTar(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, data := range entries {
		if err := writeTarMember(w, name, data); err != nil {
			t.Fatalf("writeTarMember(%s): %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	return buf.Bytes()
}
