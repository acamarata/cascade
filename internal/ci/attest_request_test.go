package ci

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRequestNodeSignatureRefusesMismatchedAnswer(t *testing.T) {
	r := newNodeAttestRig(t)
	r.run(t, "exec-1", -51, r.passExec)
	sign := func(p []byte) ([]byte, error) { return ed25519.Sign(r.ctrl, p), nil }
	if _, err := RequestNodeSignature(context.Background(), nil, sign, r.self.NodeID, r.request); err == nil {
		t.Fatal("nil call accepted")
	}
	// A node answering a different request (here: a replay of another
	// nonce's record) is refused even though its signature is genuine.
	genuine, err := r.ask(r.request)
	if err != nil {
		t.Fatal(err)
	}
	replay := func(_ context.Context, _ string, _, out any) error {
		raw, _ := json.Marshal(genuine)
		return json.Unmarshal(raw, out)
	}
	other := r.request
	other.Nonce = "nonce-2"
	_, err = RequestNodeSignature(context.Background(), replay, sign, r.self.NodeID, other)
	wantCIKind(t, err, cascade.KindIntegrity)
	_, err = RequestNodeSignature(context.Background(), replay, sign, "another-node", r.request)
	wantCIKind(t, err, cascade.KindIntegrity)
}

func TestRequestNodeSignatureSignsOnlyTheRequest(t *testing.T) {
	_, ctrl, _ := ed25519.GenerateKey(rand.Reader)
	var seen nodes.AttestSignRequest
	call := func(_ context.Context, method string, params, _ any) error {
		if method != nodes.AttestSignMethod {
			t.Fatalf("method %q", method)
		}
		seen = params.(nodes.AttestSignRequest)
		return cascade.New(cascade.KindPermissionDenied, "node refused")
	}
	req := newNodeAttestRig(t).request
	req.ControllerSig = "caller-supplied"
	_, err := RequestNodeSignature(context.Background(), call, func(p []byte) ([]byte, error) { return ed25519.Sign(ctrl, p), nil }, "node-1", req)
	wantCIKind(t, err, cascade.KindPermissionDenied)
	payload, _ := nodes.AttestSignPayload("node-1", req)
	if seen.ControllerSig == "caller-supplied" || seen.ControllerSig == "" {
		t.Fatalf("controller signature not replaced: %q", seen.ControllerSig)
	}
	b := nodes.ControllerBinding{ControllerPubKeyB64: encodeStd(ctrl.Public().(ed25519.PublicKey))}
	if err := nodes.VerifyControllerRequest(b, payload, decodeStd(t, seen.ControllerSig)); err != nil {
		t.Fatalf("request signature does not verify: %v", err)
	}
}

func encodeStd(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decodeStd(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
