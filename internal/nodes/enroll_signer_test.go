package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type recordingEnrollmentSigner struct {
	testEnvelopeSigner
	messages [][]byte
}

func (s *recordingEnrollmentSigner) Sign(ctx context.Context, msg []byte) ([]byte, error) {
	s.messages = append(s.messages, bytes.Clone(msg))
	return s.testEnvelopeSigner.Sign(ctx, msg)
}

func installationEnrollHarness(t *testing.T) (*testHarness, *recordingEnrollmentSigner) {
	t.Helper()
	h := newTestHarness(t)
	ident, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat("I", 64)))
	if err != nil {
		t.Fatal(err)
	}
	signer := &recordingEnrollmentSigner{testEnvelopeSigner: testEnvelopeSigner{priv: priv}}
	h.deps.ControllerPubKeyB64 = ident.PubKeyB64()
	h.deps.ControllerSigner = signer
	h.deps.Keystore = nil
	return h, signer
}

// TestEnrollCountersignsWithInstallationSigner proves the installation signer
// alone completes the transcript, without a controller NodeKeystore.
func TestEnrollCountersignsWithInstallationSigner(t *testing.T) {
	h, signer := installationEnrollHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	rec, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.NodeID != p.NodeID || rec.Tier != TierWorkerTrusted {
		t.Fatalf("unexpected enrollment record: %+v", rec)
	}
	tr := Transcript{NodeID: p.NodeID, NodePubKeyB64: p.NodePubKeyB64,
		ControllerPubKeyB64: h.deps.ControllerPubKeyB64, HostKeyFingerprint: p.HostKeyFingerprint}
	if len(signer.messages) != 1 || !bytes.Equal(signer.messages[0], tr.SigningPayload()) {
		t.Fatal("installation signer must sign exactly the transcript payload once")
	}
	stored, err := h.deps.Records.Get(p.NodeID)
	if err != nil || stored.NodeID != rec.NodeID {
		t.Fatalf("enrollment was not stored: %v", err)
	}
}

// TestEnrollRefusesControllerKeyMismatch proves the configured key must match
// the installation identity before host verification or any enrollment write.
func TestEnrollRefusesControllerKeyMismatch(t *testing.T) {
	h, signer := installationEnrollHarness(t)
	other, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("O", 64)))
	if err != nil {
		t.Fatal(err)
	}
	h.deps.ControllerPubKeyB64 = other.PubKeyB64()
	p := h.validPayload(t, TierWorkerTrusted)
	// An unknown host would refuse first if the installation check ran late.
	p.HostKeyOverride = ""
	_, err = EnrollNode(context.Background(), h.deps, nil, p)
	want := cascade.New(cascade.KindIntegrity, "nodes: enrollment controller key is not the installation key")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity || err.Error() != want.Error() {
		t.Fatalf("refusal = %v, want %v", err, want)
	}
	if len(signer.messages) != 0 {
		t.Fatal("installation signer was called for a mismatched controller key")
	}
	assertEnrollmentUnchanged(t, h)
}

// TestEnrollRejectsNodeSignatureOverOtherControllerKey proves the node's half
// must bind the same installation identity that countersigns enrollment.
func TestEnrollRejectsNodeSignatureOverOtherControllerKey(t *testing.T) {
	h, _ := installationEnrollHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	other, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("O", 64)))
	if err != nil {
		t.Fatal(err)
	}
	tr := Transcript{NodeID: p.NodeID, NodePubKeyB64: p.NodePubKeyB64,
		ControllerPubKeyB64: other.PubKeyB64(), HostKeyFingerprint: p.HostKeyFingerprint}
	p.NodeSignatureB64 = base64.StdEncoding.EncodeToString(SignTranscript(tr, h.nodePriv))
	_, err = EnrollNode(context.Background(), h.deps, nil, p)
	want := ErrTranscriptSignatureInvalid("node")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity || err.Error() != want.Error() {
		t.Fatalf("refusal = %v, want %v", err, want)
	}
	assertEnrollmentUnchanged(t, h)
}

func assertEnrollmentUnchanged(t *testing.T, h *testHarness) {
	t.Helper()
	records, err := h.deps.Records.List()
	if err != nil || len(records) != 0 {
		t.Fatalf("records = %v, err = %v; want no records", records, err)
	}
	err = h.knownHosts.Verify(h.host, h.hostFP, "")
	want := ErrHostKeyUnknown(h.host)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied || err.Error() != want.Error() {
		t.Fatalf("host verification = %v, want %v", err, want)
	}
}
