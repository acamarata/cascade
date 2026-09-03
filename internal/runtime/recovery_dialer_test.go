package runtime

// Purpose: tests defaultDialer (recovery.go) — the production Dialer,
//   a thin wrapper over net.DialTimeout. Previously 0% direct coverage:
//   every other recovery*_test.go file in the unit lane deliberately
//   exercises Scan's branching through the injected Dialer fake
//   (dialerStub, recovery_testutil_test.go) rather than this concrete
//   function, per Art.7.2's "no net import in an untagged _test.go
//   file" convention (recovery_test.go's file header). This file stays
//   compliant with that same rule — it never imports "net" or
//   "net/http" itself — while still calling the real defaultDialer
//   directly (a function call needs no import of the package the
//   *callee* uses internally). A short-timeout dial to a
//   near-certainly-unbound loopback port proves the wrapper actually
//   reaches net.DialTimeout and propagates its result untouched; the
//   real end-to-end "a live listener answers" path is covered
//   separately, against a genuine unix socket, by
//   recovery_integration_test.go's `//go:build integration` tests
//   (both leave Dial nil, so Scan's own resolveScanDefaults wires up
//   this exact function).
// Constraints: Art.7.2 — no "net"/"net/http" import in this file.
// SPORT: runtime/recovery (ADD, per P1-E03-W1-S05-T3 sport_updates
//   placeholder).

import (
	"testing"
	"time"
)

func TestDefaultDialer_FailsFastAgainstUnboundLoopbackPort(t *testing.T) {
	// Port 1 is a well-known reserved TCP port ("tcpmux") that nothing
	// binds to in any CI or dev environment; combined with a short
	// timeout this fails almost immediately with a real connection
	// error rather than hanging.
	closer, err := defaultDialer("tcp", "127.0.0.1:1", 200*time.Millisecond)
	if err == nil {
		t.Fatal("defaultDialer against an unbound loopback port: want error, got nil")
	}
	if closer != nil {
		t.Fatalf("defaultDialer returned a non-nil closer alongside an error: %+v", closer)
	}
}

func TestDefaultDialer_RejectsUnknownNetwork(t *testing.T) {
	// An unsupported network name is rejected by net.Dial itself before
	// any I/O, covering the same single return statement via a second,
	// independent failure mode.
	closer, err := defaultDialer("not-a-real-network", "127.0.0.1:1", 200*time.Millisecond)
	if err == nil {
		t.Fatal("defaultDialer with an unknown network: want error, got nil")
	}
	if closer != nil {
		t.Fatalf("defaultDialer returned a non-nil closer alongside an error: %+v", closer)
	}
}
