//go:build !windows && integration

package main

// Purpose: proves context.sync is reachable over the REAL daemon socket
//   built by the REAL buildRPCServer (the exact function platformDaemonRun
//   calls), following daemon_unix_context_assemble_integration_test.go's
//   own pattern for its sibling context.slice/context.show methods.
//   Registering internal/daemon.RegisterContextSyncHandler inside
//   buildRPCServer without a test that drives buildRPCServer itself would
//   leave the registration call site unverified -- this is that
//   verification, and the AGENT-BRIEF-required mutation proof (comment out
//   the registration call in registerContextEngineHandlers, rerun with
//   -tags=integration, observe the failure below; restore, rerun, observe
//   the pass) is documented in this ticket's journal with the exact
//   failure text observed.
// SPORT: cmd/cascade/daemon (CHANGED, E/S-09.T4).

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

// TestBuildRPCServer_ContextSyncReachableOverRealSocket drives
// buildRPCServer exactly as platformDaemonRun does, serves it over a real
// unix socket, and calls context.sync through internal/client.Client.Do --
// the same generic call cmd/cascade/context_sync_cmd.go's daemon-path
// branch uses.
func TestBuildRPCServer_ContextSyncReachableOverRealSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: "ctxsynce2e.sock"}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "ctxsynce2e")
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

	cwd := t.TempDir()
	var result daemon.ContextSyncResult
	if err := c.Do(ctx, daemon.ContextSyncMethod, daemon.ContextSyncParams{Cwd: cwd, CheckOnly: true}, &result); err != nil {
		t.Fatalf("context.sync over the real socket: %v", err)
	}
}
