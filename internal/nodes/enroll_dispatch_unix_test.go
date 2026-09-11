//go:build !windows

// Purpose: TestRegisterHandlers_EndToEndThroughDispatch, split out of
//   enroll_dispatch_test.go because its assertion ("an attested retry
//   must succeed") is false on Windows by design: internal/rpc's
//   platformElevationRefusal preempts the attestation flow entirely
//   there (elevation_windows.go). enroll_dispatch_windows_test.go carries
//   the real Windows-side assertion.

package nodes

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

func genHelperKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("H", 64)))
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// TestRegisterHandlers_EndToEndThroughDispatch drives the REAL production
// entry point (rpc.Registry.Dispatch, wired through rpc.ElevationMiddleware
// exactly as the daemon composition root would) and proves node.enroll is
// gated as an elevated verb (06 §5.14): a first call with no attestation
// gets ELEVATION_REQUIRED; a retry with a satisfying attestation reaches
// EnrollNode and returns a real DeviceRecord.
func TestRegisterHandlers_EndToEndThroughDispatch(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierController)
	rawArgs, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	reg := rpc.NewRegistry()
	clock := runtime.NewFixedClock(time.Unix(5000, 0))
	helperPub, helperPriv := genHelperKey(t)
	trust := rpc.MapTrustStore{"helper-fp": helperPub}
	ledger := rpc.NewNonceLedger(clock)
	reg.Use(rpc.ElevationMiddleware(ledger, trust, clock))
	RegisterHandlers(reg, h.deps, nil)

	// First call: no attestation -> ELEVATION_REQUIRED.
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.enroll", Params: rawArgs})
	if errObj == nil {
		t.Fatal("expected ELEVATION_REQUIRED on first unattested call")
	}
	nonce := extractNonce(t, errObj)

	// Retry with a satisfying attestation over the SAME args reaches the
	// real handler.
	envelope := buildAttestedEnvelope(t, rawArgs, nonce, helperPriv, clock)
	result, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.enroll", Params: envelope})
	if errObj != nil {
		t.Fatalf("attested retry must succeed: %+v", errObj)
	}
	rec, ok := result.(DeviceRecord)
	if !ok {
		t.Fatalf("result is %T, want DeviceRecord", result)
	}
	if rec.NodeID != h.nodeIdent.NodeID {
		t.Fatal("dispatched enrollment produced wrong node id")
	}
}
