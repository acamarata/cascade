// Purpose: CreateSnapshot's elevation/fail-closed-key gates, the real-
// domain snapshot flow (SQLiteCapture, no self-dialect path), second-run
// dedup, chain validation, the config-bound signing pubkey, and
// WriteManifest/ReadManifest's immutability and tamper-rejection paths.
// SPORT: internal.backup.snapshot/ADD (P1-E19-W4-S41-T2).
package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// corrupt flips one byte of the stored value at key (storage tampering).
func (m *memTarget) corrupt(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := m.data[key]
	if len(data) == 0 {
		return
	}
	cp := append([]byte{}, data...)
	cp[0] ^= 0xFF
	m.data[key] = cp
}

func setSigningKeyEnv(t *testing.T) ed25519.PublicKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(seed))
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey)
}

func TestCreateSnapshot_RequiresElevationProof(t *testing.T) {
	setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	deps := CreateSnapshotDeps{
		Target: newMemTarget(), AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(0, 0)),
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
	}
	_, err := CreateSnapshot(context.Background(), "", deps, nil)
	if err != ErrElevationRequired {
		t.Fatalf("CreateSnapshot(no proof) = %v, want ErrElevationRequired", err)
	}
}

func TestCreateSnapshot_RequiresSigningKey(t *testing.T) {
	t.Setenv(ManifestSigningKeyEnvVar, "")
	_, recipient := newTestAgeKeypair(t)
	deps := CreateSnapshotDeps{
		Target: newMemTarget(), AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(0, 0)),
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
	}
	_, err := CreateSnapshot(context.Background(), "proof-1", deps, nil)
	if err != ErrManifestSigningKeyMissing {
		t.Fatalf("CreateSnapshot(no signing key) = %v, want ErrManifestSigningKeyMissing (fail-closed: never taken unencrypted/unsigned)", err)
	}
}

func TestCreateSnapshot_RequiresDomains(t *testing.T) {
	setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	deps := CreateSnapshotDeps{Target: newMemTarget(), AgeRecipient: recipient, Clock: testkit.NewFrozenClock(time.Unix(0, 0))}
	_, err := CreateSnapshot(context.Background(), "proof-1", deps, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("CreateSnapshot(no domains) error kind = %v, want KindInvalidInput", err)
	}
}

func TestCreateSnapshot_RequiresClock(t *testing.T) {
	setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	deps := CreateSnapshotDeps{
		Target: newMemTarget(), AgeRecipient: recipient,
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
	}
	_, err := CreateSnapshot(context.Background(), "proof-1", deps, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("CreateSnapshot(nil clock) error kind = %v, want KindInvalidInput", err)
	}
}

// TestCreateSnapshot_RealDomainFlow: real SQLiteCapture (not fakeExporter),
// second run over unchanged content (dedup), a chained manifest, and the
// repo config binding the derived verification pubkey.
func TestCreateSnapshot_RealDomainFlow(t *testing.T) {
	ctx := context.Background()
	pubKey := setSigningKeyEnv(t)
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "ns", "k1", []byte("real domain content, captured via VACUUM INTO"))

	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)),
		Domains: map[string]Exporter{
			"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
	}
	first, err := CreateSnapshot(ctx, "proof-1", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot (first): %v", err)
	}
	if first.PreviousSnapshot != "" {
		t.Fatalf("first snapshot PreviousSnapshot = %q, want empty (no predecessor)", first.PreviousSnapshot)
	}
	if first.ObjectCount == 0 {
		t.Fatal("first snapshot has zero objects; the real-domain capture did not run")
	}

	cfg, err := ReadRepoConfig(ctx, target)
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if cfg.ManifestSigningPubKey != base64.StdEncoding.EncodeToString(pubKey) {
		t.Fatal("repo config was not bound to the derived manifest signing pubkey")
	}

	// Second run, same unchanged content: dedup, but a new chained manifest.
	deps.Domains = map[string]Exporter{
		"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
	}
	second, err := CreateSnapshot(ctx, "proof-2", deps, &first)
	if err != nil {
		t.Fatalf("CreateSnapshot (second): %v", err)
	}
	if second.PreviousSnapshot != first.Snapshot {
		t.Fatalf("second.PreviousSnapshot = %q, want %q (chained)", second.PreviousSnapshot, first.Snapshot)
	}
	if second.Snapshot == first.Snapshot {
		t.Fatal("second snapshot reused the first snapshot's id; snapshots must be distinct")
	}

	if err := VerifyManifest(second, pubKey, &first); err != nil {
		t.Fatalf("VerifyManifest(second, chained to first): %v", err)
	}
}

func TestCreateSnapshot_PubKeyMismatchRefuses(t *testing.T) {
	ctx := context.Background()
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	if err := WriteRepoConfig(ctx, target, RepoConfig{
		LayoutVersion: CurrentLayoutVersion, AgeRecipient: recipient,
		ManifestSigningPubKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)),
	}); err != nil {
		t.Fatalf("seed repo config: %v", err)
	}
	setSigningKeyEnv(t) // a DIFFERENT key than the one already bound above
	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock:   testkit.NewFrozenClock(time.Unix(0, 0)),
		Domains: map[string]Exporter{"d": fakeExporter{data: []byte("x")}},
	}
	_, err := CreateSnapshot(ctx, "proof-1", deps, nil)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("CreateSnapshot(bound to a different pubkey) error kind = %v, want KindConflict", err)
	}
}

func TestWriteManifest_RefusesOverwrite(t *testing.T) {
	ctx := context.Background()
	priv, _ := newTestSigningKeypair(t)
	_, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	m := Manifest{Snapshot: "snap-dup", Entries: testEntries()}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); err != nil {
		t.Fatalf("WriteManifest (first): %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("WriteManifest (duplicate) error kind = %v, want KindConflict (immutable: written once)", err)
	}
}

func TestReadManifest_RoundTrip(t *testing.T) {
	ctx := context.Background()
	priv, pub := newTestSigningKeypair(t)
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	m := Manifest{Snapshot: "snap-rt", Entries: testEntries(), Encryption: EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"}}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(ctx, target, identity, m.Snapshot, pub, nil)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.Snapshot != m.Snapshot || got.RootHash != m.RootHash {
		t.Fatalf("ReadManifest round trip mismatch: got %+v, want %+v", got, m)
	}
}

func TestReadManifest_NotFoundRefuses(t *testing.T) {
	ctx := context.Background()
	_, pub := newTestSigningKeypair(t)
	identity, _ := newTestAgeKeypair(t)
	_, err := ReadManifest(ctx, newMemTarget(), identity, "no-such-snapshot", pub, nil)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("ReadManifest(absent) error kind = %v, want KindNotFound", err)
	}
}

// TestReadManifest_WrongKeySignedManifestRefuses: a manifest signed with a
// WRONG key must be refused through the SAME ReadManifest path S-41.T4/
// S-42.T4 consume, not just by VerifyManifest in isolation.
func TestReadManifest_WrongKeySignedManifestRefuses(t *testing.T) {
	ctx := context.Background()
	wrongPriv, _ := newTestSigningKeypair(t)
	_, rightPub := newTestSigningKeypair(t)
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	m := Manifest{Snapshot: "snap-wrongkey", Entries: testEntries(), Encryption: EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"}}
	if err := SignManifest(&m, wrongPriv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	_, err := ReadManifest(ctx, target, identity, m.Snapshot, rightPub, nil)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("ReadManifest(wrong-key-signed manifest) error kind = %v, want KindIntegrity", err)
	}
}

// TestReadManifest_UnsignedManifestRefuses: an unsigned manifest, distinct
// from a wrong-key signature.
func TestReadManifest_UnsignedManifestRefuses(t *testing.T) {
	ctx := context.Background()
	_, pub := newTestSigningKeypair(t)
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	root, err := computeRootHash(testEntries())
	if err != nil {
		t.Fatalf("computeRootHash: %v", err)
	}
	m := Manifest{
		Snapshot: "snap-unsigned", Entries: testEntries(), RootHash: root,
		Encryption:    EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"},
		SignatureAlgo: "ed25519", // schema-shaped but Signature left empty
	}
	data, merr := json.Marshal(m)
	if merr != nil {
		t.Fatalf("encode: %v", merr)
	}
	encrypted, eerr := Encrypt(recipient, data)
	if eerr != nil {
		t.Fatalf("Encrypt: %v", eerr)
	}
	if perr := target.Put(ctx, manifestKey(m.Snapshot), bytes.NewReader(encrypted)); perr != nil {
		t.Fatalf("Put: %v", perr)
	}
	_, err = ReadManifest(ctx, target, identity, m.Snapshot, pub, nil)
	if err == nil {
		t.Fatal("ReadManifest(unsigned manifest) = nil error, want a refusal")
	}
}

// TestReadManifest_TamperedCiphertextRefuses: the AEAD tag failure path.
func TestReadManifest_TamperedCiphertextRefuses(t *testing.T) {
	ctx := context.Background()
	priv, pub := newTestSigningKeypair(t)
	identity, recipient := newTestAgeKeypair(t)
	target := newMemTarget()
	m := Manifest{Snapshot: "snap-tamper", Entries: testEntries(), Encryption: EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"}}
	if err := SignManifest(&m, priv); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	target.corrupt(manifestKey(m.Snapshot))
	_, err := ReadManifest(ctx, target, identity, m.Snapshot, pub, nil)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("ReadManifest(tampered ciphertext) error kind = %v, want KindIntegrity", err)
	}
}
