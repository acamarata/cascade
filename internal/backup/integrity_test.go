// Purpose: VerifyIntegrity's fail-closed proof: a well-formed two-snapshot
//
//	chain passes, then each required corruption mode (tampered object,
//	truncated object, missing object, a corrupted ancestor reached only
//	by the chain walk, a missing chain predecessor, a self-referential
//	chain, absent key material) is proven against a REAL stored artifact
//	to refuse — with a passing baseline established first so the
//	negative assertion cannot be vacuous.
//
// SPORT: internal.backup.integrity/ADDED (P1-E19-W4-S41-T4).

package backup

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// truncate cuts the stored value at key in half — a real, malformed
// artifact distinct from corrupt's bit-flip.
func (m *memTarget) truncate(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := m.data[key]
	if len(data) < 2 {
		return
	}
	m.data[key] = append([]byte{}, data[:len(data)/2]...)
}

// firstObjectKey returns one arbitrary stored objects/ key, so a test can
// corrupt a real chunk without hard-coding a hash.
func firstObjectKey(t *testing.T, target *memTarget) string {
	t.Helper()
	keys, err := target.List(context.Background(), "objects/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("List(objects/) = %v, %v; want at least one stored object", keys, err)
	}
	return keys[0]
}

// chainedSnapshots builds two real, chained snapshots (via CreateSnapshot,
// the same production path snapshot_test.go's
// TestCreateSnapshot_RealDomainFlow exercises) over one real SQLite
// domain, returning the target, the verification pubkey, and both
// manifests.
func chainedSnapshots(t *testing.T) (target *memTarget, pubKey ed25519.PublicKey, first, second Manifest) {
	t.Helper()
	ctx := context.Background()
	pub := setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target = newMemTarget()
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "context", "k1", []byte("real integrity-gate payload"))

	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_100, 0)),
		Domains: map[string]Exporter{
			"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
	}
	first, err := CreateSnapshot(ctx, "proof-1", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot (first): %v", err)
	}
	deps.Domains = map[string]Exporter{
		"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
	}
	second, err = CreateSnapshot(ctx, "proof-2", deps, &first)
	if err != nil {
		t.Fatalf("CreateSnapshot (second): %v", err)
	}
	return target, pub, first, second
}

func TestIntegrityGate_PassesOnWellFormedChain(t *testing.T) {
	target, pub, _, second := chainedSnapshots(t)
	m, report, err := VerifyIntegrity(context.Background(), GateOptions{Target: target, PubKey: pub}, second.Snapshot)
	if err != nil {
		t.Fatalf("VerifyIntegrity(well-formed chain): %v", err)
	}
	if m.Snapshot != second.Snapshot {
		t.Fatalf("VerifyIntegrity returned snapshot %q, want %q", m.Snapshot, second.Snapshot)
	}
	if report.ChainDepth != 2 {
		t.Fatalf("report.ChainDepth = %d, want 2 (first+second)", report.ChainDepth)
	}
	if report.ObjectsVerified == 0 {
		t.Fatal("report.ObjectsVerified = 0; the completeness pass did not run")
	}
}

// TestIntegrityGate_ChainWalkDetectsCorruptedAncestor is the marquee
// chain-walk proof: VerifyIntegrity(second) succeeds against the
// unmodified target (the baseline, so this negative assertion cannot be
// vacuous), then the FIRST snapshot's manifest bytes are bit-flipped in
// the real stored target and the exact same call now refuses — proving
// the gate walks back to ancestors rather than checking only the snapshot
// named in the call.
func TestIntegrityGate_ChainWalkDetectsCorruptedAncestor(t *testing.T) {
	target, pub, first, second := chainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("baseline VerifyIntegrity(second) before corruption: %v", err)
	}

	target.corrupt(manifestKey(first.Snapshot))

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err == nil {
		t.Fatal("VerifyIntegrity(second) after corrupting ancestor's manifest = nil error, want a refusal")
	}
}

func TestIntegrityGate_DetectsTamperedObject(t *testing.T) {
	target, pub, _, second := chainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("baseline VerifyIntegrity before tampering: %v", err)
	}
	target.corrupt(firstObjectKey(t, target))

	_, _, err := VerifyIntegrity(ctx, opts, second.Snapshot)
	if err == nil {
		t.Fatal("VerifyIntegrity after tampering with a stored object = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("error kind check failed, want KindIntegrity; err = %v", err)
	}
}

func TestIntegrityGate_DetectsTruncatedObject(t *testing.T) {
	target, pub, _, second := chainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("baseline VerifyIntegrity before truncation: %v", err)
	}
	target.truncate(firstObjectKey(t, target))

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err == nil {
		t.Fatal("VerifyIntegrity after truncating a stored object = nil error, want a refusal")
	}
}

func TestIntegrityGate_DetectsMissingObject(t *testing.T) {
	target, pub, _, second := chainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("baseline VerifyIntegrity before deletion: %v", err)
	}
	objectKey := firstObjectKey(t, target)
	if err := target.Delete(ctx, objectKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, _, err := VerifyIntegrity(ctx, opts, second.Snapshot)
	if err == nil {
		t.Fatal("VerifyIntegrity after deleting a stored object = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("error kind check failed, want KindNotFound; err = %v", err)
	}
}

func TestIntegrityGate_MissingChainPredecessorRefuses(t *testing.T) {
	target, pub, first, second := chainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("baseline VerifyIntegrity before deleting predecessor: %v", err)
	}
	if err := target.Delete(ctx, manifestKey(first.Snapshot)); err != nil {
		t.Fatalf("Delete manifest: %v", err)
	}

	_, _, err := VerifyIntegrity(ctx, opts, second.Snapshot)
	if err == nil {
		t.Fatal("VerifyIntegrity with a deleted predecessor manifest = nil error, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("error kind check failed, want KindNotFound (missing ancestor manifest); err = %v", err)
	}
}

func TestIntegrityGate_MissingKeyMaterialRefuses(t *testing.T) {
	target := newMemTarget()
	pub := setSigningKeyEnv(t)
	_, _, err := VerifyIntegrity(context.Background(), GateOptions{Target: target, PubKey: pub}, SnapshotID("nonexistent"))
	if err != ErrAgeIdentityMissing {
		t.Fatalf("VerifyIntegrity with no AgeIdentityEnvVar = %v, want ErrAgeIdentityMissing", err)
	}
}

// TestIntegrityGate_ChainCycleRefuses drives loadManifestChain's cycle
// guard through a real Target holding a manifest that names itself as its
// own previous_snapshot — a shape CreateSnapshot never produces (ids are
// fresh ULIDs) but a corrupted/adversarial repo could, proving the guard
// is reachable and correct rather than dead code.
func TestIntegrityGate_ChainCycleRefuses(t *testing.T) {
	ctx := context.Background()
	signingKey, pub := newTestSigningKeypair(t)
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(signingKey.Seed()))
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target := newMemTarget()

	m := Manifest{Snapshot: "self-cycle", PreviousSnapshot: "self-cycle",
		Encryption: EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"}}
	if err := SignManifest(&m, signingKey); err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	if err := WriteManifest(ctx, target, recipient, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	opts := GateOptions{Target: target, PubKey: pub}
	if _, _, err := VerifyIntegrity(ctx, opts, m.Snapshot); err != ErrGateChainCycle {
		t.Fatalf("VerifyIntegrity(self-referential chain) = %v, want ErrGateChainCycle", err)
	}
}
