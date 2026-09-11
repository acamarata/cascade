package nodes

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func buildValidTranscript(t *testing.T) (Transcript, string) {
	t.Helper()
	nodeID, nodePriv := mustIdentity(t, "node")
	ctrlID, ctrlPriv := mustIdentity(t, "ctrl")
	hostFP := "deadbeef"

	tr := Transcript{
		NodeID:              nodeID.NodeID,
		NodePubKeyB64:       nodeID.PubKeyB64(),
		ControllerPubKeyB64: ctrlID.PubKeyB64(),
		HostKeyFingerprint:  hostFP,
	}
	tr.NodeSignature = SignTranscript(tr, nodePriv)
	tr.ControllerSignature = SignTranscript(tr, ctrlPriv)
	return tr, hostFP
}

func mustIdentity(t *testing.T, seed string) (Identity, []byte) {
	t.Helper()
	id, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat(seed, 64)))
	if err != nil {
		t.Fatal(err)
	}
	return id, priv
}

func TestVerifyTranscriptValid(t *testing.T) {
	tr, hostFP := buildValidTranscript(t)
	if err := VerifyTranscript(tr, hostFP); err != nil {
		t.Fatalf("unexpected error verifying valid transcript: %v", err)
	}
}

func TestVerifyTranscriptSignatureMismatchRefused(t *testing.T) {
	tr, hostFP := buildValidTranscript(t)
	// Corrupt the node signature.
	tr.NodeSignature[0] ^= 0xFF
	err := VerifyTranscript(tr, hostFP)
	if err == nil {
		t.Fatal("expected refusal for corrupted node signature")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

func TestVerifyTranscriptControllerSignatureMismatchRefused(t *testing.T) {
	tr, hostFP := buildValidTranscript(t)
	tr.ControllerSignature[0] ^= 0xFF
	if err := VerifyTranscript(tr, hostFP); err == nil {
		t.Fatal("expected refusal for corrupted controller signature")
	}
}

func TestVerifyTranscriptHostKeyMismatchRefused(t *testing.T) {
	tr, _ := buildValidTranscript(t)
	err := VerifyTranscript(tr, "a-different-fingerprint")
	if err == nil {
		t.Fatal("expected refusal for host key fingerprint mismatch")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

func TestVerifyTranscriptTamperedPayloadRefused(t *testing.T) {
	tr, hostFP := buildValidTranscript(t)
	// Tamper with a bound field after signing: signatures no longer cover
	// this payload.
	tr.HostKeyFingerprint = hostFP // keep expected match...
	tr.NodeID = "attacker-supplied-different-id"
	if err := VerifyTranscript(tr, hostFP); err == nil {
		t.Fatal("expected refusal for tampered NodeID (payload no longer matches signature)")
	}
}

func TestVerifyTranscriptMalformedPubKeyRefused(t *testing.T) {
	tr, hostFP := buildValidTranscript(t)
	tr.NodePubKeyB64 = "not-valid-base64!!!"
	if err := VerifyTranscript(tr, hostFP); err == nil {
		t.Fatal("expected refusal for malformed public key")
	}
}

func TestSigningPayloadUnambiguous(t *testing.T) {
	a := Transcript{NodeID: "ab", NodePubKeyB64: "cd"}
	b := Transcript{NodeID: "abc", NodePubKeyB64: "d"}
	if string(a.SigningPayload()) == string(b.SigningPayload()) {
		t.Fatal("distinct field splits must not collide in the signing payload")
	}
}
