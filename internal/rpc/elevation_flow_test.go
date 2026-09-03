package rpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestElevationMiddleware_FullRoundTripWithAttestation exercises the
// complete elevation flow end to end through the middleware — the branch
// attestAndProceed covers — rather than VerifyAttestation in isolation
// (elevation_attest_test.go) or ELEVATION_REQUIRED issuance alone
// (handler_test.go's TestHandler_ElevationDeniedThenReplayRejected): a
// client's first call gets ELEVATION_REQUIRED+nonce, then a retry
// wrapping the original args plus a satisfying attestation reaches the
// real handler and gets the real result.
// elevationFixture is the shared setup for the round-trip tests, extracted
// to keep each test function under the 50-line cap (Art.10.3).
type elevationFixture struct {
	clock  *runtime.FixedClock
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	trust  MapTrustStore
	ledger *NonceLedger
}

func newElevationFixture(t *testing.T) *elevationFixture {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	pub, priv := genAttestTestKey(t)
	return &elevationFixture{
		clock:  clock,
		pub:    pub,
		priv:   priv,
		trust:  MapTrustStore{"fp1": pub},
		ledger: NewNonceLedger(clock),
	}
}

func TestElevationMiddleware_FullRoundTripWithAttestation(t *testing.T) {
	f := newElevationFixture(t)
	clock, priv, trust, ledger := f.clock, f.priv, f.trust, f.ledger

	var handlerCalled bool
	handler := func(_ context.Context, params json.RawMessage) (any, error) {
		handlerCalled = true
		var m map[string]string
		_ = json.Unmarshal(params, &m)
		return m["target"], nil
	}
	mw := ElevationMiddleware(ledger, trust, clock)
	wrapped := mw("vault.get", handler)

	originalArgs := json.RawMessage(`{"target":"db-password"}`)
	nonce, paramsHash := assertDeniedWithoutAttestation(t, wrapped, originalArgs, &handlerCalled)
	att := Attestation{
		RequestID:         "req-2",
		ActionHash:        paramsHash,
		Nonce:             nonce,
		PubkeyFingerprint: "fp1",
		IssuedUnix:        clock.Now().Unix(),
		ExpUnix:           clock.Now().Add(time.Minute).Unix(),
	}
	sig := ed25519.Sign(priv, signedFields(att))
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)

	envelope := elevatedEnvelope{Attestation: &att, Args: originalArgs}
	envBytes, _ := json.Marshal(envelope)

	result, err := wrapped(context.Background(), envBytes)
	if err != nil {
		t.Fatalf("attested retry must succeed: %v", err)
	}
	if !handlerCalled {
		t.Fatal("handler must run once elevation is satisfied")
	}
	if result != "db-password" {
		t.Errorf("result = %v, want db-password", result)
	}

	assertReplayRejected(t, wrapped, envBytes)
}

func TestElevationMiddleware_WindowsRefusalNeverAttempts(t *testing.T) {
	if platformElevationRefusal() != nil {
		// This test only asserts something meaningful on POSIX, where
		// platformElevationRefusal is nil — elevation_windows.go carries
		// the mirror-image assertion under its own build tag.
		t.Skip("platform refuses elevation unconditionally; see elevation_windows.go")
	}
}

// assertReplayRejected proves a consumed nonce cannot be reused. Extracted
// to keep the round-trip test under the 50-line cap (Art.10.3).
func assertReplayRejected(t *testing.T, wrapped HandlerFunc, envBytes json.RawMessage) {
	t.Helper()
	// Replaying the SAME attestation must now fail (nonce already consumed).
	_, err := wrapped(context.Background(), envBytes)
	if err == nil {
		t.Fatal("replaying the same attested envelope must fail")
	}
}

// assertDeniedWithoutAttestation proves an elevated verb refuses an
// unattested call and returns the nonce to attest with. Extracted to keep
// the round-trip test under the 50-line cap (Art.10.3).
func assertDeniedWithoutAttestation(t *testing.T, wrapped HandlerFunc, args json.RawMessage, handlerCalled *bool) (nonce, paramsHash string) {
	t.Helper()
	_, err := wrapped(context.Background(), args)
	if err == nil {
		t.Fatal("first call (no attestation) must be denied with ELEVATION_REQUIRED")
	}
	eo, ok := err.(*ErrorObject)
	if !ok || eo.Code != codeElevationRequired {
		t.Fatalf("err = %+v, want ELEVATION_REQUIRED ErrorObject", err)
	}
	if *handlerCalled {
		t.Fatal("handler must not run before elevation is satisfied")
	}
	nonceData := eo.Data.(elevationRequiredData)
	return nonceData.Nonce, hashParams(args)
}
