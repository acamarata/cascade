//go:build !windows && integration

package main

// Purpose: proves context.slice and context.show are reachable over the
//   REAL daemon socket built by the REAL buildRPCServer (the exact
//   function platformDaemonRun calls), following
//   daemon_unix_context_scope_integration_test.go's own pattern for the
//   sibling context.scope.show method. Registering
//   internal/daemon.RegisterContextAssembleHandler inside buildRPCServer
//   without a test that drives buildRPCServer itself would leave the
//   registration call site unverified -- this is that verification, and
//   the AGENT-BRIEF-required mutation proof (comment out the registration
//   call, rerun, observe the failure below; restore, rerun, observe the
//   pass) is documented in this ticket's journal with the exact failure
//   text observed.
// SPORT: cmd/cascade/daemon (CHANGED, E/S-09.T2).

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_ContextSliceShowReachableOverRealSocket drives
// buildRPCServer exactly as platformDaemonRun does, serves it over a real
// unix socket, and calls context.slice and context.show through
// internal/client.Client.Do -- the same generic call cmd/cascade/
// context_cmd.go's daemon-path branches use.
func TestBuildRPCServer_ContextSliceShowReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: "ctxslicee2e.sock"}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	// A short os.MkdirTemp directory keeps the socket path under macOS/BSD's
	// ~104-byte unix-socket cap, matching the context.scope.show sibling
	// test's own serveRecallRPC-style helper.
	sockDir, err := os.MkdirTemp("", "ctxslicee2e")
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

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	t.Run("context.show", func(t *testing.T) {
		var result daemon.ContextShowResult
		if err := c.Do(ctx, daemon.ContextShowMethod, daemon.ContextAssembleParams{Cwd: cwd}, &result); err != nil {
			t.Fatalf("context.show over the real socket: %v", err)
		}
	})

	t.Run("context.slice", func(t *testing.T) {
		var result daemon.ContextSliceResult
		if err := c.Do(ctx, daemon.ContextSliceMethod, daemon.ContextAssembleParams{Cwd: cwd}, &result); err != nil {
			t.Fatalf("context.slice over the real socket: %v", err)
		}
	})
}
