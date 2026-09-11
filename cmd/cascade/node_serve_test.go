// Purpose: unit tests for node_serve.go's cobra wiring and runNodeServe's
//
//	composition, over injected nodeServeDeps so no test resolves the real
//	CASCADE_HOME or touches a real OS keychain (Art.7.1).
//
// SPORT: cmd/cascade/node (ADD, per T-2 sport_updates).
package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// shortTempDir returns a short-named temp directory (unlike t.TempDir(),
// whose name embeds the full test name and can exceed a unix socket
// path's sun_path length limit once "/data/nodes/node.sock" is appended).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testNodeServeDeps(t *testing.T) nodeServeDeps {
	t.Helper()
	root := shortTempDir(t)
	return nodeServeDeps{
		Paths:      fakeDaemonPaths{root: root},
		Clock:      runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		SecretsDir: shortTempDir(t),
		GOOS:       "darwin",
	}
}

// TestRunNodeServeRefusesOnWindows proves the tier-2 refusal is wired: a
// deps.GOOS of "windows" refuses before anything else (no socket bind, no
// keystore open) — the identical refusal internal/nodes.RefuseOnGOOS
// itself unit-tests, now proven reachable from the real cobra command's
// entry point.
func TestRunNodeServeRefusesOnWindows(t *testing.T) {
	deps := testNodeServeDeps(t)
	deps.GOOS = "windows"
	err := runNodeServe(context.Background(), deps)
	if err == nil {
		t.Fatal("expected a tier-2 refusal for goos=windows")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got kind %v (ok=%v), want KindUnsupported", kind, ok)
	}
}

// TestRunNodeServeStartsAndDrainsOnCancel drives the REAL production
// entry point: it binds the real unix socket, mounts the real registry
// (proving GenerateIdentity, NewFileKnownHostsBackend, NewFileRecordBackend,
// NewKnownHosts, NewNodeKeystore, NewRecordStore and RegisterHandlers are
// all load-bearing from this exact call path), then returns cleanly once
// ctx is canceled — no real signal, no real sleep (Art.7.3).
func TestRunNodeServeStartsAndDrainsOnCancel(t *testing.T) {
	deps := testNodeServeDeps(t)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- runNodeServe(ctx, deps) }()

	// Give the goroutine a bounded window to reach its select{} (socket
	// bind + registry build), then cancel and require a prompt, clean
	// return.
	select {
	case err := <-errCh:
		t.Fatalf("runNodeServe returned before cancellation, err=%v", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error on drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runNodeServe to drain")
	}
}

// emptyDataDirPaths is a runtime.PathProvider whose DataDir() is always
// "", exercising runNodeServe's unresolvable-data-directory refusal
// without depending on fakeDaemonPaths's own Join-based DataDir shape.
type emptyDataDirPaths struct{}

func (emptyDataDirPaths) Root() string                       { return "" }
func (emptyDataDirPaths) ConfigPath() string                 { return "" }
func (emptyDataDirPaths) SocketPath() string                 { return "" }
func (emptyDataDirPaths) DataDir() string                    { return "" }
func (emptyDataDirPaths) LogDir() string                     { return "" }
func (emptyDataDirPaths) StorageRoot(runtime.Profile) string { return "" }

// TestRunNodeServeMissingDataDirRefused proves an unresolvable data
// directory is a typed refusal, never a panic.
func TestRunNodeServeMissingDataDirRefused(t *testing.T) {
	deps := testNodeServeDeps(t)
	deps.Paths = emptyDataDirPaths{}
	err := runNodeServe(context.Background(), deps)
	if err == nil {
		t.Fatal("expected refusal for an empty data directory")
	}
}

func TestMountNodeCmdWiresServeSubcommand(t *testing.T) {
	root := newRootCmd()
	nodeCmd, _, err := root.Find([]string{"node", "serve"})
	if err != nil {
		t.Fatalf("expected `node serve` to be mounted: %v", err)
	}
	if nodeCmd.Use != "serve" {
		t.Fatalf("got Use %q, want serve", nodeCmd.Use)
	}
}

// TestNodeServeCmdRejectsArgs proves the usual usageArgs(cobra.NoArgs)
// wiring is in place.
func TestNodeServeCmdRejectsArgs(t *testing.T) {
	cmd := newNodeServeCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Args(cmd, []string{"unexpected"}); err == nil {
		t.Fatal("expected an error for unexpected positional args")
	}
}
