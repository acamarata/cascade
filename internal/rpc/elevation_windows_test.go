//go:build windows

package rpc

// Purpose: proves elevation_windows.go's tier-2 refusal actually RUNS (not
//   merely compiles) on the Windows CI lane, per R-14.131 ("every ticket
//   whose correctness depends on platform-specific ... behaviour MUST have
//   at least one test that actually RUNS on the Windows lane, not merely
//   builds"). This function has no OS syscall dependency (pure Go logic),
//   but the contract's own text calls out "Windows elevated-verb
//   middleware returns ELEVATION_REQUIRED with platform actionable
//   message (build-tagged test)" as an explicit AC — this file is that
//   test.

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPlatformElevationRefusal_Windows(t *testing.T) {
	refusal := platformElevationRefusal()
	if refusal == nil {
		t.Fatal("Windows must always refuse elevation")
	}
	if refusal.Code != codeElevationRequired {
		t.Errorf("Code = %d, want %d", refusal.Code, codeElevationRequired)
	}
	if refusal.Message == "" {
		t.Fatal("refusal message must be non-empty and actionable")
	}
}

func TestElevationMiddleware_WindowsAlwaysRefuses(t *testing.T) {
	ledger := NewNonceLedger(nil)
	handlerCalled := false
	handler := func(ctx context.Context, _ json.RawMessage) (any, error) {
		handlerCalled = true
		return "should not run", nil
	}
	mw := ElevationMiddleware(ledger, MapTrustStore{}, nil)
	_, err := mw("vault.get", handler)(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ELEVATION_REQUIRED refusal on Windows")
	}
	if handlerCalled {
		t.Fatal("handler must never run on Windows for an elevated verb")
	}
}
