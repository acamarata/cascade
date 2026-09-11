package nodes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testHarness builds a full in-process EnrollDeps fixture: a controller
// identity already stored in a NodeKeystore, an empty RecordStore and
// KnownHosts, ready for EnrollNode / RegisterHandlers tests.
type testHarness struct {
	deps       EnrollDeps
	keystore   *NodeKeystore
	knownHosts *KnownHosts
	nodeIdent  Identity
	nodePriv   ed25519.PrivateKey
	hostFP     string
	host       string
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Now())
	records := NewRecordStore(newMemRecordBackend(), clock)
	knownHosts := NewKnownHosts(newMemKnownHostsBackend())

	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	ctrlIdent, ctrlPriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("K", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(context.Background(), ctrlIdent.NodeID, ctrlPriv); err != nil {
		t.Fatal(err)
	}

	nodeIdent, nodePriv, err := GenerateIdentity(strings.NewReader(strings.Repeat("N", 64)))
	if err != nil {
		t.Fatal(err)
	}

	return &testHarness{
		deps: EnrollDeps{
			Records:             records,
			KnownHosts:          knownHosts,
			Keystore:            ks,
			ControllerNodeID:    ctrlIdent.NodeID,
			ControllerPubKeyB64: ctrlIdent.PubKeyB64(),
		},
		keystore:   ks,
		knownHosts: knownHosts,
		nodeIdent:  nodeIdent,
		nodePriv:   nodePriv,
		hostFP:     "aabbccddeeff",
		host:       "worker@host1",
	}
}

// validPayload builds a correctly-formed, correctly-signed EnrollPayload
// for h's fixture identity, requesting tier.
func (h *testHarness) validPayload(t *testing.T, tier Tier) EnrollPayload {
	t.Helper()
	p := EnrollPayload{
		NodeID:             h.nodeIdent.NodeID,
		NodePubKeyB64:      h.nodeIdent.PubKeyB64(),
		TrustTier:          string(tier),
		Host:               h.host,
		HostKeyFingerprint: h.hostFP,
		HostKeyOverride:    h.hostFP, // first-seen host: operator confirms out of band
	}
	tr := Transcript{
		NodeID:              p.NodeID,
		NodePubKeyB64:       p.NodePubKeyB64,
		ControllerPubKeyB64: h.deps.ControllerPubKeyB64,
		HostKeyFingerprint:  p.HostKeyFingerprint,
	}
	sig := SignTranscript(tr, h.nodePriv)
	p.NodeSignatureB64 = base64.StdEncoding.EncodeToString(sig)
	return p
}

// TestEnrollNode_HappyPath proves EnrollNode admits a correctly-formed,
// correctly-signed, correctly-pinned enrollment end to end.
func TestEnrollNode_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)

	rec, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Tier != TierWorkerTrusted {
		t.Fatalf("tier = %q, want worker-trusted", rec.Tier)
	}
	if rec.NodeID != h.nodeIdent.NodeID {
		t.Fatal("node id mismatch in resulting record")
	}
}

func TestEnrollRequiresTrustTier(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, "")
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal for missing trust_tier")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}
}

func TestEnrollPairedDeviceRefused(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierPairedDevice)
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal enrolling paired-device")
	}
}

func TestEnrollUnknownHostKeyRefused(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	p.HostKeyOverride = "" // no out-of-band confirmation, host never pinned
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal for unknown host key")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindPermissionDenied {
		t.Fatalf("expected KindPermissionDenied, got %v (ok=%v)", k, ok)
	}
}

func TestEnrollTranscriptSignatureRefused(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	// Corrupt the node's signature: transcript verification must fail.
	sig, _ := base64.StdEncoding.DecodeString(p.NodeSignatureB64)
	sig[0] ^= 0xFF
	p.NodeSignatureB64 = base64.StdEncoding.EncodeToString(sig)

	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal for invalid transcript signature")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("expected KindIntegrity, got %v (ok=%v)", k, ok)
	}
}

func TestEnrollDuplicateConflict(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	if _, err := EnrollNode(context.Background(), h.deps, nil, p); err != nil {
		t.Fatal(err)
	}
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected conflict on re-enroll")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindConflict {
		t.Fatalf("expected KindConflict, got %v (ok=%v)", k, ok)
	}
}

// TestEnrollDaemonlessRefusesWithoutPrecondition proves an elevated verb
// run under embedded/daemonless mode refuses when the local
// helper-enrolled/authenticator-available precondition does not hold
// (D/S-07.T4 §D-24), fail closed by default (nil precondition).
func TestEnrollDaemonlessRefusesWithoutPrecondition(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})

	_, err := EnrollNode(ctx, h.deps, nil, p)
	if err == nil {
		t.Fatal("expected daemonless elevation refusal")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindElevationRequired {
		t.Fatalf("expected KindElevationRequired, got %v (ok=%v)", k, ok)
	}
}

func TestEnrollDaemonlessSucceedsWithPrecondition(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	precondition := func() (bool, bool) { return true, true }

	_, err := EnrollNode(ctx, h.deps, precondition, p)
	if err != nil {
		t.Fatalf("unexpected error with satisfied daemonless precondition: %v", err)
	}
}

func TestDecodeEnrollHandshakePayload_Malformed(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("{"),
		[]byte("not json at all"),
		[]byte(`{"node_id":"x"}`),       // missing required fields
		[]byte(`{"unknown_field":"x"}`), // unknown field, DisallowUnknownFields
		[]byte(`{"node_id":"x","node_pubkey_b64":"y","host":"h","host_key_fingerprint":"f","node_signature_b64":"s"}{"trailing":1}`),
	}
	for i, c := range cases {
		if _, err := DecodeEnrollHandshakePayload(c); err == nil {
			t.Errorf("case %d: expected refusal, got nil error", i)
		}
	}
}

func TestDecodeEnrollHandshakePayload_TooLarge(t *testing.T) {
	huge := make([]byte, maxEnrollPayloadBytes+1)
	if _, err := DecodeEnrollHandshakePayload(huge); err == nil {
		t.Fatal("expected refusal for oversized payload")
	}
}

func TestDecodeEnrollHandshakePayload_NeverPanics(t *testing.T) {
	inputs := []string{
		`{`, `}`, `null`, `[]`, `"just a string"`, `12345`, `{{{{`,
		string([]byte{0x00, 0xff, 0xfe}),
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on input %q: %v", in, r)
				}
			}()
			_, _ = DecodeEnrollHandshakePayload([]byte(in))
		}()
	}
}

func TestEnrollNodeInvalidSignatureBase64Refused(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierWorkerTrusted)
	p.NodeSignatureB64 = "not-valid-base64!!!"
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal for malformed node_signature_b64")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}
}

func TestEnrollNodeInvalidControllerPubKeyRefused(t *testing.T) {
	h := newTestHarness(t)
	h.deps.ControllerPubKeyB64 = "not-valid-base64!!!"
	p := h.validPayload(t, TierWorkerTrusted)
	_, err := EnrollNode(context.Background(), h.deps, nil, p)
	if err == nil {
		t.Fatal("expected refusal for malformed controller public key")
	}
}
