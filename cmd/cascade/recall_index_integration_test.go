//go:build !windows && integration

// Purpose: the composition-root proof for `cascade recall index`. It
// calls the REAL buildRPCServer — the function platformDaemonRun calls —
// over a REAL store and a REAL unix socket, and drives the REAL cobra
// commands through the PRODUCTION SDK call seam (clientRecallCall)
// against it, exactly recall_integration_test.go's pattern for
// `cascade recall` itself.
//
// What this catches that nothing else does: recall.index.* being
// registered on a registry a test built, rather than the one
// buildRPCServer builds inside internal/daemon.RegisterRecallIndexHandler.
// Deleting that call (or its invocation in daemon_unix_run.go) turns this
// red with "method not found".
//
// Constraints: build-tagged "integration" (imports "net"/"net/http",
// which the no-network unit lane forbids, Art.7.2).
//
// SPORT: cmd.cascade.cmd.recall.index (ADD, P1-E06-W2-S11-T4).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/providers/sqlite"
)

// startRecallIndexDaemon builds the daemon's REAL RPC server over a REAL
// on-disk store (unlike startRecallDaemon in recall_integration_test.go,
// which passes a nil store and so never registers recall.index.* at
// all — see RegisterRecallIndexHandler's nil-store degradation).
func startRecallIndexDaemon(t *testing.T) recallDeps {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "recallidxcli")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	root := t.TempDir()
	paths := fakeMemoryPaths{root: root}
	clock := runtime.SystemClock{}

	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	driver, err := sqlite.Open(context.Background(), filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	bus := events.New(storetest.NewMemStore(), clock)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	settings := daemon.Settings{SocketPath: filepath.Join(sockDir, "daemon.sock")}

	server, _, _, err := buildRPCServer(bus, clock, logger, settings, paths, nil, driver)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	ln, err := net.Listen("unix", settings.SocketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server.ReadHeaderTimeout = 5 * time.Second
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	return recallDeps{
		Paths:   fakeMemoryPaths{root: sockDir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Call:    clientRecallCall,
	}
}

// TestRecallIndexIsReachableOnTheDaemonTheCompositionRootBuilds drives
// all four verbs against the real daemon over the real socket. A daemon
// that never registered recall.index.* would answer "method not found"
// for every one of them; this test's whole point is to fail that way the
// moment the registration is missing.
func TestRecallIndexIsReachableOnTheDaemonTheCompositionRootBuilds(t *testing.T) {
	deps := startRecallIndexDaemon(t)
	for _, verb := range []string{"rebuild", "verify", "migrate", "update"} {
		t.Run(verb, func(t *testing.T) {
			out, err := runRecallAgainstDaemon(t, deps, "index", verb)
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "method not found") {
				t.Fatalf("recall.index.%s is not registered on the daemon's own registry: %v", verb, err)
			}
			_ = out // verify/update on a zero-corpus index may legitimately refuse; only "method not found" is fatal here
		})
	}
}

// TestRecallIndexRebuildThenVerifyEndToEnd proves rebuild and verify
// agree over the real daemon: an empty (zero registered sources) rebuild
// converges, and verify then reports it clean.
func TestRecallIndexRebuildThenVerifyEndToEnd(t *testing.T) {
	deps := startRecallIndexDaemon(t)
	if _, err := runRecallAgainstDaemon(t, deps, "index", "rebuild"); err != nil {
		t.Fatalf("recall index rebuild: %v", err)
	}
	out, err := runRecallAgainstDaemon(t, deps, "index", "verify")
	if err != nil {
		t.Fatalf("recall index verify: %v", err)
	}
	if strings.Contains(out, "method not found") {
		t.Fatalf("verify did not run: %s", out)
	}
}
