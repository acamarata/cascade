package nodes

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func signedFrame(t *testing.T, priv ed25519.PrivateKey, nodeID, enrollmentID string, seq uint64) HeartbeatFrame {
	t.Helper()
	f := HeartbeatFrame{NodeID: nodeID, EnrollmentID: enrollmentID, Sequence: seq, Report: validReport()}
	sig, err := SignHeartbeatFrame(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	f.SignatureB64 = base64.StdEncoding.EncodeToString(sig)
	return f
}

func enrolledRecord(t *testing.T) (DeviceRecord, ed25519.PrivateKey, Identity) {
	t.Helper()
	id, priv, err := ed25519GenTest(t)
	if err != nil {
		t.Fatal(err)
	}
	rec := DeviceRecord{NodeID: id.NodeID, PubKeyB64: id.PubKeyB64(), Tier: TierWorkerTrusted, EnrolledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return rec, priv, id
}

func ed25519GenTest(t *testing.T) (Identity, ed25519.PrivateKey, error) {
	t.Helper()
	return GenerateIdentity(strings.NewReader(strings.Repeat("z", 64)))
}

func TestVerifyHeartbeatFrameAccepted(t *testing.T) {
	rec, priv, id := enrolledRecord(t)
	f := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1)
	if err := VerifyHeartbeatFrame(f, rec, 0); err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

func TestHeartbeatSpoofedSignerRejected(t *testing.T) {
	rec, _, id := enrolledRecord(t)
	// A DIFFERENT identity signs a frame claiming to be id.NodeID.
	attacker, attackerPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("q", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if attacker.NodeID == id.NodeID {
		t.Fatal("test fixture collision, pick different seeds")
	}
	f := signedFrame(t, attackerPriv, id.NodeID, DeriveEnrollmentID(rec), 1)
	err = VerifyHeartbeatFrame(f, rec, 0)
	if err == nil {
		t.Fatal("expected spoofed-signer refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("got kind %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

func TestHeartbeatReplayedSequenceRejected(t *testing.T) {
	rec, priv, id := enrolledRecord(t)
	f := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 5)
	// lastSeq == 5: a frame at or below 5 must be refused as a replay.
	err := VerifyHeartbeatFrame(f, rec, 5)
	if err == nil {
		t.Fatal("expected replayed-sequence refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("got kind %v (ok=%v), want KindConflict", kind, ok)
	}
	// A strictly greater sequence is accepted.
	f2 := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 6)
	if err := VerifyHeartbeatFrame(f2, rec, 5); err != nil {
		t.Fatalf("sequence 6 after lastSeq 5 should be accepted: %v", err)
	}
}

func TestHeartbeatRevokedKeyRejected(t *testing.T) {
	rec, priv, id := enrolledRecord(t)
	rec.RevokedKeys = append(rec.RevokedKeys, fingerprintOfKey(id.PubKey))
	f := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1)
	err := VerifyHeartbeatFrame(f, rec, 0)
	if err == nil {
		t.Fatal("expected revoked-key refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("got kind %v (ok=%v), want KindPermissionDenied", kind, ok)
	}
}

func TestHeartbeatEnrollmentMismatchRejected(t *testing.T) {
	rec, priv, id := enrolledRecord(t)
	f := signedFrame(t, priv, id.NodeID, "not-the-real-enrollment-id", 1)
	err := VerifyHeartbeatFrame(f, rec, 0)
	if err == nil {
		t.Fatal("expected enrollment-id mismatch refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("got kind %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

func TestVerifyHeartbeatFrameCorruptStoredKey(t *testing.T) {
	rec, priv, id := enrolledRecord(t)
	rec.PubKeyB64 = "not-valid-base64!!"
	f := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1)
	if err := VerifyHeartbeatFrame(f, rec, 0); err == nil {
		t.Fatal("expected refusal for corrupt stored public key")
	}
}

func TestVerifyHeartbeatFrameMalformedSignature(t *testing.T) {
	rec, _, id := enrolledRecord(t)
	f := HeartbeatFrame{NodeID: id.NodeID, EnrollmentID: DeriveEnrollmentID(rec), Sequence: 1, Report: validReport(), SignatureB64: "%%%not-base64"}
	if err := VerifyHeartbeatFrame(f, rec, 0); err == nil {
		t.Fatal("expected refusal for malformed signature")
	}
}

func TestDeriveEnrollmentIDDeterministic(t *testing.T) {
	rec := DeviceRecord{NodeID: "abc", EnrolledAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	id1 := DeriveEnrollmentID(rec)
	id2 := DeriveEnrollmentID(rec)
	if id1 != id2 {
		t.Fatal("DeriveEnrollmentID must be deterministic for the same record")
	}
	other := rec
	other.EnrolledAt = rec.EnrolledAt.Add(time.Second)
	if DeriveEnrollmentID(other) == id1 {
		t.Fatal("a different EnrolledAt must derive a different enrollment id")
	}
}

func TestSequenceStoreAdvanceAndLast(t *testing.T) {
	s := NewSequenceStore()
	if got := s.Last("n1"); got != 0 {
		t.Fatalf("got %d, want 0 for unseen node", got)
	}
	s.Advance("n1", 3)
	if got := s.Last("n1"); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	// a different node id is tracked independently.
	if got := s.Last("n2"); got != 0 {
		t.Fatalf("got %d, want 0 for a different node id", got)
	}
}
