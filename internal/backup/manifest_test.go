// Purpose: manifest.go's signing-key resolution, root-hash computation,
//
//	sign/verify round trip, schema decode, and every tamper-detection
//	refusal path (invalid signature, wrong-key signature, unsigned
//	manifest, root_hash mismatch, unknown encryption version, broken
//	chain) plus FuzzSnapshotManifestDecode (06 §5.7, R-21.266).
//
// SPORT: internal.backup.manifest/ADD (P1-E19-W4-S41-T2).
package backup

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func newTestSigningKeypair(t testing.TB) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv, pub
}

func testEntries() []ManifestEntry {
	return []ManifestEntry{
		{Domain: "journal", Refs: []ManifestObjectRef{{Hash: "aa", Size: 10}, {Hash: "bb", Size: 20}}},
		{Domain: "sessions", Refs: []ManifestObjectRef{{Hash: "cc", Size: 5}}},
	}
}

// TestManifestSigningKey_GeneratedHere is provenance for HOW the signature
// fixtures in this file were produced: a fresh ed25519.GenerateKey(rand.Reader)
// per test (crypto/rand, the Go standard library's CSPRNG), NOT a fixture
// this package's own code produced once and now merely replays -
// interoperability with a real minisign/age-adjacent Ed25519 verifier
// follows from using crypto/ed25519's standard Sign/Verify (the same
// primitive OpenSSH, minisign and age's own signing tools implement), not
// from a self-referential round trip. See ManifestSigningKey's own env-ref
// resolution (used by CreateSnapshot, snapshot_test.go) for the
// production-shaped path; this file's direct ed25519.GenerateKey calls
// are test-only key material, never shipped.
func TestManifestSigningKey_GeneratedHere(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	msg := []byte("provenance check")
	sig := ed25519.Sign(priv, msg)
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatal("a signature from crypto/ed25519.Sign must verify with crypto/ed25519.Verify")
	}
}

func TestManifestSigningKey_MissingEnvRefuses(t *testing.T) {
	t.Setenv(ManifestSigningKeyEnvVar, "")
	if _, err := ManifestSigningKey(); err == nil {
		t.Fatal("ManifestSigningKey() with unset env = nil error, want ErrManifestSigningKeyMissing")
	}
}

func TestManifestSigningKey_MalformedEnvRefuses(t *testing.T) {
	t.Setenv(ManifestSigningKeyEnvVar, "not-valid-base64!!!")
	if _, err := ManifestSigningKey(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ManifestSigningKey(malformed base64) error kind = %v, want KindInvalidInput", err)
	}
}

func TestManifestSigningKey_WrongLengthSeedRefuses(t *testing.T) {
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString([]byte("too-short")))
	if _, err := ManifestSigningKey(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ManifestSigningKey(short seed) error kind = %v, want KindInvalidInput", err)
	}
}

func TestManifestSigningKey_ValidEnvResolves(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(seed))
	key, err := ManifestSigningKey()
	if err != nil {
		t.Fatalf("ManifestSigningKey: %v", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		t.Fatalf("len(key) = %d, want %d", len(key), ed25519.PrivateKeySize)
	}
}

func TestSignAndVerifyManifest_RoundTrip(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if m.RootHash == "" || m.Signature == "" || m.SignatureAlgo != "ed25519" {
		t.Fatalf("SignManifest left fields unset: %+v", m)
	}
	if m.ObjectCount != 3 {
		t.Fatalf("ObjectCount = %d, want 3", m.ObjectCount)
	}
	if err := VerifyManifest(m, pub, nil); err != nil {
		t.Fatalf("VerifyManifest(valid): %v", err)
	}
}

// TestVerifyManifest_WrongKeyRefuses proves a manifest signed by one key
// fails verification against a DIFFERENT key's public half - the
// authenticity property R-14.58 exists for: a writer without the
// vault-held signing key cannot produce a validly-verifying manifest.
func TestVerifyManifest_WrongKeyRefuses(t *testing.T) {
	priv, _ := newTestSigningKeypair(t)
	_, wrongPub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	err := VerifyManifest(m, wrongPub, nil)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyManifest(wrong key) error kind = %v, want KindIntegrity", err)
	}
}

// TestVerifyManifest_UnsignedRefuses proves an unsigned manifest (empty
// Signature, e.g. one a writer skipped SignManifest for) never verifies -
// there is no "verification skipped" path.
func TestVerifyManifest_UnsignedRefuses(t *testing.T) {
	_, pub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	root, err := computeRootHash(m.Entries)
	if err != nil {
		t.Fatalf("computeRootHash: %v", err)
	}
	m.RootHash = root // signature left empty
	if err := VerifyManifest(m, pub, nil); err == nil {
		t.Fatal("VerifyManifest(unsigned) = nil, want an error")
	}
}

func TestVerifyManifest_RootHashMismatchRefuses(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	m.Entries[0].Refs[0].Size = 999999 // mutate AFTER signing: entries no longer match the signed root hash
	if err := VerifyManifest(m, pub, nil); err != ErrManifestRootHashMismatch {
		t.Fatalf("VerifyManifest(mutated entries) = %v, want ErrManifestRootHashMismatch", err)
	}
}

func TestDecodeManifestUnverified_UnknownEncryptionVersionRefuses(t *testing.T) {
	priv, _ := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries(), Encryption: EncryptionInfo{Version: 99}}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = DecodeManifestUnverified(data)
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("DecodeManifestUnverified(unknown encryption version) error kind = %v, want KindUnsupported", err)
	}
}

func TestVerifyManifest_ChainBroken_FirstSnapshotWithPredecessor(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-2", PreviousSnapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := VerifyManifest(m, pub, nil); err != ErrManifestChainBroken {
		t.Fatalf("VerifyManifest(claims a predecessor, none supplied) = %v, want ErrManifestChainBroken", err)
	}
}

func TestVerifyManifest_ChainBroken_WrongPredecessor(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	prev := Manifest{Snapshot: "snap-1"}
	m := Manifest{Snapshot: "snap-2", PreviousSnapshot: "snap-999", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := VerifyManifest(m, pub, &prev); err != ErrManifestChainBroken {
		t.Fatalf("VerifyManifest(wrong predecessor) = %v, want ErrManifestChainBroken", err)
	}
}

func TestVerifyManifest_ChainOK(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	prev := Manifest{Snapshot: "snap-1"}
	m := Manifest{Snapshot: "snap-2", PreviousSnapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := VerifyManifest(m, pub, &prev); err != nil {
		t.Fatalf("VerifyManifest(correct chain): %v", err)
	}
}

func TestVerifyManifest_MalformedSignatureBase64Refuses(t *testing.T) {
	priv, pub := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	m.Signature = "not-valid-base64!!!"
	if err := VerifyManifest(m, pub, nil); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyManifest(malformed signature) error kind = %v, want KindIntegrity", err)
	}
}

func TestVerifyManifest_InvalidPublicKeyLengthRefuses(t *testing.T) {
	priv, _ := newTestSigningKeypair(t)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := VerifyManifest(m, []byte("too-short"), nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("VerifyManifest(bad pubkey length) error kind = %v, want KindInvalidInput", err)
	}
}

func TestSignManifest_InvalidPrivateKeyLengthRefuses(t *testing.T) {
	m := Manifest{Entries: testEntries()}
	if err := SignManifest(&m, []byte("too-short")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("SignManifest(bad key length) error kind = %v, want KindInvalidInput", err)
	}
}

func TestComputeRootHash_Deterministic(t *testing.T) {
	a, err := computeRootHash(testEntries())
	if err != nil {
		t.Fatalf("computeRootHash: %v", err)
	}
	b, err := computeRootHash(testEntries())
	if err != nil {
		t.Fatalf("computeRootHash: %v", err)
	}
	if a != b {
		t.Fatalf("computeRootHash is non-deterministic over identical input: %q != %q", a, b)
	}
}

func TestComputeRootHash_RefusesUnhexableHash(t *testing.T) {
	_, err := computeRootHash([]ManifestEntry{{Domain: "d", Refs: []ManifestObjectRef{{Hash: "not-hex"}}}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("computeRootHash(non-hex ref hash) error kind = %v, want KindInvalidInput", err)
	}
}

// FuzzSnapshotManifestDecode is this ticket's own-parser fuzz target
// (06-FORGE-SPEC §5.7, R-21.266): DecodeManifestUnverified must never
// panic on any input, malformed or adversarial.
func FuzzSnapshotManifestDecode(f *testing.F) {
	priv, _ := newTestSigningKeypair(f)
	m := Manifest{Snapshot: "snap-1", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		f.Fatalf("SignManifest: %v", err)
	}
	valid, err := json.Marshal(m)
	if err != nil {
		f.Fatalf("marshal: %v", err)
	}
	f.Add(valid)
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"snapshot":"","entries":[]}`))
	f.Add([]byte(`{"snapshot":"s","encryption":{"version":99},"signature":"x","signature_algorithm":"ed25519"}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeManifestUnverified(data) // must never panic
	})
}
