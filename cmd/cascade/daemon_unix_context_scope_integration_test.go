//go:build !windows && integration

package main

// Purpose: proves context.scope.show is reachable over the REAL daemon
//   socket built by the REAL buildRPCServer (the exact function
//   platformDaemonRun calls), following
//   daemon_unix_mcp_integration_test.go's own pattern for the MCP
//   dispatcher. Registering internal/daemon.RegisterContextScopeHandler
//   inside buildRPCServer without a test that drives buildRPCServer
//   itself would leave the registration call site unverified -- this is
//   that verification, and the AGENT-BRIEF-required mutation proof
//   (comment out the registration call, rerun, observe the failure
//   below; restore, rerun, observe the pass) is documented in this
//   ticket's journal with the exact failure text observed.
// SPORT: cmd/cascade/daemon (CHANGED, E/S-08.T4).

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_ContextScopeShowReachableOverRealSocket drives
// buildRPCServer exactly as platformDaemonRun does, serves it over a real
// unix socket, and calls context.scope.show through the real
// internal/client.Client SDK -- the same client cmd/cascade/
// context_scope.go's daemon-path branch uses.
func TestBuildRPCServer_ContextScopeShowReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: "ctxscopee2e.sock"}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	// A unix socket path is capped near 104 bytes on macOS/BSD, and
	// t.TempDir() embeds this test's own (long) name -- a short
	// os.MkdirTemp directory keeps the socket path under that cap,
	// matching internal/retrieval/recall/rpc_integration_test.go's own
	// serveRecallRPC helper.
	sockDir, err := os.MkdirTemp("", "ctxscopee2e")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	c := client.New(sockPath, client.DialFunc(dial), 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := c.ContextScopeShow(ctx, scope.ScopeShowParams{Session: "s1"})
	if err != nil {
		t.Fatalf("ContextScopeShow over the real socket: %v", err)
	}
	if got.Session != "s1" {
		t.Errorf("Session = %q, want %q", got.Session, "s1")
	}
}
