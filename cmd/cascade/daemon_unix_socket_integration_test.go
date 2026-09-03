//go:build !windows && integration

// Purpose: socketDialable's success path (something IS accepting
//
//	connections at path) — needs a real net.Listener, which Art.7.2's
//	no-network-unit-lane gate (internal/build's hygiene.go) forbids
//	importing "net" for outside an integration-tagged file. Split out of
//	daemon_unix_status_test.go into its own `-tags=integration` sibling,
//	matching internal/daemon's own daemon_unix_integration_test.go split
//	for exactly this reason.
//
// Constraints: no real daemon process — a plain net.Listener at a path
//
//	under a short temp dir, closed via t.Cleanup.
//
// SPORT: cmd/cascade/daemon (coverage-only addition, no sport_updates —
//
//	this file adds no new production surface).
package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestSocketDialable_TrueWhenListening is TestSocketDialable's (daemon_
// unix_test.go) missing success-path counterpart: something IS accepting
// connections at path. Uses a short dedicated temp dir, not t.TempDir()
// (whose path embeds this test's own long name), because a unix socket
// path must fit in sockaddr_un.sun_path (~104 bytes on Darwin).
func TestSocketDialable_TrueWhenListening(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	if !socketDialable(path) {
		t.Error("socketDialable = false against a socket a listener is actively accepting on")
	}
}
