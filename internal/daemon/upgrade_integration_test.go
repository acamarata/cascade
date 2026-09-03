//go:build !windows && integration

package daemon

// Purpose: the one upgrade_test.go-family case that needs a real "net"
// socket (Art.7.2 forbids that import in the default unit lane) — proves
// HARD REQUIREMENT 1's second half against an actual listener: a
// connection attempted after Drain has closed it is refused by the OS,
// never silently accepted and dropped. The unit lane (upgrade_conntracker_
// test.go's TestDrainGrace/TestDrainTimeout) already proves Drain's
// wait/force-close semantics against a fakeListener; this file only adds
// the real-socket half, mirroring daemon_unix_integration_test.go's
// established split.
// SPORT: internal/daemon (ADD, per T-5 sport_updates).

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestDrain_RealSocket_RefusesAfterClose proves a real net.Listener, once
// handed to Drain, refuses a subsequent connection attempt at the
// transport level rather than accepting and silently dropping it.
func TestDrain_RealSocket_RefusesAfterClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	m, _ := newTestManager(t, nil, nil)
	if err := m.Drain(context.Background(), ln, nil, time.Second); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if _, err := net.Dial("tcp", addr); err == nil {
		t.Fatal("dial after Drain: want refused, got a connection")
	}
}
