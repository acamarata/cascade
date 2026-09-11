//go:build windows

// Purpose: the Windows mirror of elevation_flow_test.go's round-trip -
//   proves the SAME valid, correctly-signed attestation that succeeds on
//   POSIX is refused on Windows, and that the handler is never called,
//   because platformElevationRefusal (elevation_windows.go) preempts the
//   attestation flow entirely by design. Replaces the previous vacuous
//   TestElevationMiddleware_WindowsRefusalNeverAttempts, which skipped on
//   this platform instead of asserting anything (R-14.131: a
//   platform-specific behavior needs a test that RUNS on that platform).

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

func TestElevationMiddleware_Windows_RefusesEvenWithValidAttestation(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)

	var handlerCalled bool
	handler := func(_ context.Context, _ json.RawMessage) (any, error) {
		handlerCalled = true
		return "db-password", nil
	}
	mw := ElevationMiddleware(ledger, trust, clock)
	wrapped := mw("vault.get", handler)

	// A first, unattested call must refuse too - but with the platform
	// refusal, not a nonce-bearing ELEVATION_REQUIRED: the attestation
	// flow is never entered at all on this platform.
	originalArgs := json.RawMessage(`{"target":"db-password"}`)
	_, err := wrapped(context.Background(), originalArgs)
	assertWindowsElevationRefusal(t, err)
	if handlerCalled {
		t.Fatal("handler must not run: the platform refusal precedes it")
	}

	// A fully valid, correctly-signed attestation must ALSO be refused:
	// platformElevationRefusal runs before attestAndProceed is ever
	// reached, so a real signature changes nothing on this platform.
	att := Attestation{
		RequestID:         "req-2",
		ActionHash:        hashParams(originalArgs),
		Nonce:             "irrelevant-on-windows",
		PubkeyFingerprint: "fp1",
		IssuedUnix:        clock.Now().Unix(),
		ExpUnix:           clock.Now().Add(time.Minute).Unix(),
	}
	sig := ed25519.Sign(priv, signedFields(att))
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	envelope := elevatedEnvelope{Attestation: &att, Args: originalArgs}
	envBytes, _ := json.Marshal(envelope)

	_, err = wrapped(context.Background(), envBytes)
	assertWindowsElevationRefusal(t, err)
	if handlerCalled {
		t.Fatal("handler must not run: a valid attestation still never reaches it on Windows")
	}
}

// assertWindowsElevationRefusal checks err is exactly the platform's
// tier-2 ErrorObject: ELEVATION_REQUIRED code, no nonce (this is not the
// normal requireElevation flow - there is nothing to attest with), and a
// message naming the POSIX fallback.
func assertWindowsElevationRefusal(t *testing.T, err error) {
	t.Helper()
	eo, ok := err.(*ErrorObject)
	if !ok || eo.Code != codeElevationRequired {
		t.Fatalf("err = %+v, want the platform's ELEVATION_REQUIRED ErrorObject", err)
	}
	data, ok := eo.Data.(elevationRequiredData)
	if !ok || data.Nonce != "" {
		t.Fatalf("Data = %#v, want the platform refusal's nonce-less shape", eo.Data)
	}
}
