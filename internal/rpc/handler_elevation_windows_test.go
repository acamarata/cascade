//go:build windows

// Purpose: the Windows mirror of handler_elevation_unix_test.go - the
//   same elevated RPC call gets ELEVATION_REQUIRED, but in the platform's
//   own nonce-less shape (elevation_windows.go's platformElevationRefusal
//   preempts the normal requireElevation/nonce-ledger flow entirely), and
//   the handler is never invoked.

package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestHandler_Windows_ElevationDeniedHasNoNonce(t *testing.T) {
	withOwnerUID(t, 501)
	h, reg := newTestHandler()
	var handlerCalled bool
	reg.Register("vault.get", func(_ context.Context, _ json.RawMessage) (any, error) {
		handlerCalled = true
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
	if handlerCalled {
		t.Fatal("handler must not run: the platform refusal precedes the elevated verb entirely")
	}
	data, ok := env.Error.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %#v, want a map", env.Error.Data)
	}
	if nonce, present := data["nonce"]; present && nonce != "" {
		t.Fatalf("Data carried a nonce (%v); the platform refusal issues none to attest with", nonce)
	}
}
