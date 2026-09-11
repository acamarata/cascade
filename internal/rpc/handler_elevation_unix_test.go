//go:build !windows

// Purpose: TestHandler_ElevationDeniedThenReplayRejected, split out of
//   handler_test.go because its assertion (a nonce-bearing ELEVATION_REQUIRED
//   from the normal requireElevation flow) is POSIX-only: on Windows,
//   elevation_windows.go's platformElevationRefusal preempts that flow with
//   a different, nonce-less shape. handler_elevation_windows_test.go
//   carries the real Windows-side assertion.

package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestHandler_ElevationDeniedThenReplayRejected(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	reg.Register("vault.get", func(_ context.Context, _ json.RawMessage) (any, error) {
		return "secret", nil
	})
	clock := runtime.NewFixedClock(time.Unix(3000, 0))
	ledger := NewNonceLedger(clock)
	trust := MapTrustStore{}
	reg.Use(ElevationMiddleware(ledger, trust, clock))

	rec := doRPC(ctxWithPeerCred(501, true), h, `{"jsonrpc":"2.0","method":"vault.get","id":1}`)
	var env ResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || env.Error.Code != codeElevationRequired {
		t.Fatalf("Error = %+v, want ELEVATION_REQUIRED (%d)", env.Error, codeElevationRequired)
	}
	data, ok := env.Error.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %#v, want a map carrying nonce", env.Error.Data)
	}
	nonce, _ := data["nonce"].(string)
	if nonce == "" {
		t.Fatal("ELEVATION_REQUIRED response must carry a non-empty nonce")
	}

	// A second, direct Consume of the SAME nonce must fail — the
	// single-use ledger proof this ticket's AC requires, exercised
	// through the same object the handler used.
	if err := ledger.Consume(nonce, "vault.get", hashParams(nil), clock.Now()); err != nil {
		t.Fatalf("first direct Consume should still succeed: %v", err)
	}
	if err := ledger.Consume(nonce, "vault.get", hashParams(nil), clock.Now()); err == nil {
		t.Fatal("replaying the same nonce must fail")
	}
}
