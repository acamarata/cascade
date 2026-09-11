package nodes

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func rotateFixture(t *testing.T) (*RecordStore, DeviceRecord, Identity) {
	t.Helper()
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now()))
	id := testIdentity(t, "r")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	return store, rec, id
}

// TestKeyRotationPreservesRecord proves rotation preserves trust_tier and
// sync cursors while swapping the active key and moving the old one to
// the revoked set.
func TestKeyRotationPreservesRecord(t *testing.T) {
	store, rec, id := rotateFixture(t)
	// Seed sync cursors to prove they survive rotation untouched.
	rec.SyncCursors = map[string]string{"context": "cursor-1"}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}

	newID, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}

	// The signature must come from the CURRENT identity's private key,
	// which is `id`'s (not a fresh generation) — regenerate deterministically.
	_, currentPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("r", 64)))
	if err != nil {
		t.Fatal(err)
	}
	sig := SignRotationRequest(id.NodeID, newID.PubKeyB64(), currentPriv)

	updated, err := store.Rotate(id.NodeID, newID.PubKeyB64(), sig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Tier != TierWorkerTrusted {
		t.Fatalf("tier not preserved: got %q", updated.Tier)
	}
	if updated.SyncCursors["context"] != "cursor-1" {
		t.Fatal("sync cursors not preserved across rotation")
	}
	if updated.PubKeyB64 != newID.PubKeyB64() {
		t.Fatal("active key not updated")
	}
	if len(updated.RevokedKeys) != 1 {
		t.Fatalf("expected 1 revoked key, got %d", len(updated.RevokedKeys))
	}
}

func TestRotateWrongSignerRefused(t *testing.T) {
	store, _, id := rotateFixture(t)
	newID, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	// Sign with the NEW key instead of the current one -- must be refused.
	_, newPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	sig := SignRotationRequest(id.NodeID, newID.PubKeyB64(), newPriv)

	_, err = store.Rotate(id.NodeID, newID.PubKeyB64(), sig)
	if err == nil {
		t.Fatal("expected refusal for rotation signed by a non-current key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

// TestRevokedKeyRefused proves a heartbeat/handshake signed by a revoked
// key is refused.
func TestRevokedKeyRefused(t *testing.T) {
	store, _, id := rotateFixture(t)
	updated, err := store.Revoke(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !SignatureRevoked(updated, id.PubKeyB64()) {
		t.Fatal("expected the revoked key to be reported as revoked")
	}
	// A fresh, unrelated key must not be reported as revoked.
	other := testIdentity(t, "z")
	if SignatureRevoked(updated, other.PubKeyB64()) {
		t.Fatal("an unrelated key must not be reported as revoked")
	}
}

func TestRevokePreservesRecord(t *testing.T) {
	store, rec, id := rotateFixture(t)
	rec.SyncCursors = map[string]string{"memory": "cursor-9"}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Revoke(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tier != TierWorkerTrusted {
		t.Fatal("tier not preserved across revoke")
	}
	if updated.SyncCursors["memory"] != "cursor-9" {
		t.Fatal("sync cursors not preserved across revoke")
	}
}

func TestRotateAfterRevokeRefused(t *testing.T) {
	store, _, id := rotateFixture(t)
	if _, err := store.Revoke(id.NodeID); err != nil {
		t.Fatal(err)
	}
	newID, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("s", 64)))
	if err != nil {
		t.Fatal(err)
	}
	_, currentPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("r", 64)))
	if err != nil {
		t.Fatal(err)
	}
	sig := SignRotationRequest(id.NodeID, newID.PubKeyB64(), currentPriv)
	_, err = store.Rotate(id.NodeID, newID.PubKeyB64(), sig)
	if err == nil {
		t.Fatal("expected refusal rotating a revoked key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindPermissionDenied {
		t.Fatalf("expected KindPermissionDenied, got %v (ok=%v)", k, ok)
	}
}

func TestSignatureRevokedMalformedKeyFailsClosed(t *testing.T) {
	rec := DeviceRecord{RevokedKeys: nil}
	if !SignatureRevoked(rec, "not-valid-base64!!!") {
		t.Fatal("an unparseable key must be treated as revoked (fail closed)")
	}
}

func TestRevokeCorruptStoredKeyRefused(t *testing.T) {
	backend := newMemRecordBackend()
	store := NewRecordStore(backend, testkit.NewFrozenClock(time.Now()))
	backend.records["broken-node"] = DeviceRecord{NodeID: "broken-node", PubKeyB64: "not-valid-base64!!!", Tier: TierWorkerTrusted}

	_, err := store.Revoke("broken-node")
	if err == nil {
		t.Fatal("expected refusal revoking a record with a corrupt stored public key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

func TestRevokePutErrorPropagates(t *testing.T) {
	store, _, id := rotateFixture(t)
	backend := store.backend.(*memRecordBackend)
	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")
	if _, err := store.Revoke(id.NodeID); err == nil {
		t.Fatal("expected error when backend save fails")
	}
}
