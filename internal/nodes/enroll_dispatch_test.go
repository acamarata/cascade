package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// This file drives the wired production-caller proof (RegisterHandlers
// through the real rpc.Registry.Dispatch entry point, with the real
// rpc.ElevationMiddleware and a real attestation round trip) plus the
// FuzzEnrollHandshakePayload target; split out of enroll_test.go to stay
// under the 300-line file cap.

// TestRegisterHandlers_EndToEndThroughDispatch and its genHelperKey
// helper moved to enroll_dispatch_unix_test.go (!windows): the assertion
// that an attested retry succeeds is false on Windows by design
// (internal/rpc's platformElevationRefusal preempts the attestation flow
// entirely there). enroll_dispatch_windows_test.go carries the real
// Windows-side assertion.

// TestRegisterHandlers_WithoutWiring_MethodNotFound proves the wiring in
// TestRegisterHandlers_EndToEndThroughDispatch is load-bearing: an
// otherwise-identical registry that never calls RegisterHandlers refuses
// node.enroll with method-not-found, never reaching EnrollNode at all.
// This is the "prove the test can fail by removing the wiring" check the
// build brief requires.
func TestRegisterHandlers_WithoutWiring_MethodNotFound(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierController)
	rawArgs, _ := json.Marshal(p)

	reg := rpc.NewRegistry() // RegisterHandlers deliberately NOT called
	_ = h
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.enroll", Params: rawArgs})
	if errObj == nil {
		t.Fatal("expected method-not-found with no handler registered")
	}
	if errObj.Code != -32601 {
		t.Fatalf("errObj.Code = %d, want -32601 (method not found)", errObj.Code)
	}
}

// extractNonce reads the ELEVATION_REQUIRED error's nonce. errObj.Data
// carries rpc's unexported elevationRequiredData struct directly (this is
// an in-process Dispatch call, not a real wire round trip), so this test
// re-marshals it into a local struct sharing the same json tags rather
// than reaching into rpc's unexported type.
func extractNonce(t *testing.T, errObj *rpc.ErrorObject) string {
	t.Helper()
	raw, err := json.Marshal(errObj.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Nonce == "" {
		t.Fatalf("no nonce in ELEVATION_REQUIRED error data: %#v", errObj.Data)
	}
	return data.Nonce
}

// hashParamsForTest replicates internal/rpc's unexported hashParams
// exactly (sha256 hex of the raw params bytes) so this test can build a
// satisfying Attestation.ActionHash without reaching into rpc's
// unexported symbols.
func hashParamsForTest(params json.RawMessage) string {
	sum := sha256.Sum256(params)
	return hex.EncodeToString(sum[:])
}

// signedAttestationFieldsForTest replicates internal/rpc's unexported
// signedFields: the canonical, alphabetically-keyed-by-tag JSON encoding
// of every Attestation field except SigB64.
func signedAttestationFieldsForTest(a rpc.Attestation) []byte {
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, _ := json.Marshal(signable{
		ActionHash: a.ActionHash, ExpUnix: a.ExpUnix, IssuedUnix: a.IssuedUnix,
		Nonce: a.Nonce, PubkeyFingerprint: a.PubkeyFingerprint, RequestID: a.RequestID,
	})
	return b
}

func buildAttestedEnvelope(t *testing.T, args json.RawMessage, nonce string, priv ed25519.PrivateKey, clock *runtime.FixedClock) json.RawMessage {
	t.Helper()
	att := rpc.Attestation{
		RequestID:         "req-1",
		ActionHash:        hashParamsForTest(args),
		Nonce:             nonce,
		PubkeyFingerprint: "helper-fp",
		IssuedUnix:        clock.Now().Unix(),
		ExpUnix:           clock.Now().Add(time.Minute).Unix(),
	}
	sig := ed25519.Sign(priv, signedAttestationFieldsForTest(att))
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)

	env := map[string]interface{}{"_attestation": att, "_args": args}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FuzzEnrollHandshakePayload exercises DecodeEnrollHandshakePayload against
// malformed handshake payloads: the enrollment decoder is attacker-facing
// pre-trust input (06 §5.7), and this target's only invariant is "never
// panic" -- DecodeEnrollHandshakePayload's own error return is the
// fail-closed contract; the fuzzer proves it holds for inputs no unit test
// enumerates by hand.
func FuzzEnrollHandshakePayload(f *testing.F) {
	f.Add([]byte(`{"node_id":"abc","node_pubkey_b64":"AAAA","trust_tier":"worker-trusted","host":"h","host_key_fingerprint":"fp","node_signature_b64":"sig"}`))
	f.Add([]byte(""))
	f.Add([]byte("{malformed"))
	f.Add([]byte("null"))
	f.Add([]byte(`{"node_id":""}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeEnrollHandshakePayload panicked on %q: %v", data, r)
			}
		}()
		_, _ = DecodeEnrollHandshakePayload(data)
	})
}
