//go:build !windows && integration

// Purpose: end-to-end tests for `cascade status` that dial a REAL unix
//
//	socket serving the REAL internal/rpc.Registry/Handler pipeline: the
//	golden human table, the --json versioned envelope's shape, the typed
//	error when the daemon is not running, "second invocation after the
//	daemon stops", and automation parity (CASCADE_NO_INPUT has no
//	effect). Build-tagged "integration" (not the default unit lane)
//	because internal/build's no-network-unit-lane gate (Art.7.2) forbids
//	an untagged _test.go file from importing "net"/"net/http" - see
//	status_test.go for this command's network-free unit tests and
//	daemon_unix_status_integration_test.go for the composition-root
//	wiring-reachability proof.
//
// SPORT: cmd/cascade/status (ADD, per T-1 sport_updates).
package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedStatusResponse is the deterministic payload every fake server in
// this file returns, so the golden fixture and the --json assertions never
// depend on a real PID, a real uptime, or a real temp-dir socket path
// (those are exercised for real by internal/daemon/status_test.go and
// daemon_unix_status_integration_test.go instead - this file's job is the
// CLI-side dial/decode/render path).
var fixedStatusResponse = daemon.StatusResponse{
	Version: "1.2.3-test",
	Daemon: daemon.StatusDaemonFields{
		PID:         4242,
		UptimeS:     12.5,
		Connections: 2,
		SocketPath:  "/var/run/cascade/daemon.sock",
	},
	Health: "ok",
}

// startFakeStatusServer binds a real unix socket under a short temp dir
// and serves status.get through the REAL rpc.Registry/rpc.Handler
// pipeline: the same pipeline the real daemon composition root uses,
// returning a statusDeps whose DialContext reaches it. t.Cleanup closes
// the server and removes the temp dir.
func startFakeStatusServer(t *testing.T, resp daemon.StatusResponse) (statusDeps, string) {
	t.Helper()
	// A short-lived os.MkdirTemp("", ...) directory, not t.TempDir(): the
	// latter nests under the test's own (long) name, and a unix socket
	// path is capped at ~104 bytes on macOS/BSD.
	dir, err := os.MkdirTemp("", "cst")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	registry := rpc.NewRegistry()
	registry.Register(daemon.StatusMethod, func(_ context.Context, _ json.RawMessage) (any, error) {
		return resp, nil
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: rpc.NewHandler(registry), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	deps := statusDeps{
		Paths:   fakeDaemonPaths{root: filepath.Dir(sockPath)},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		DialContext: func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}
	return deps, sockPath
}

// runStatusCmd executes `status` [--json] against deps and returns
// stdout+stderr combined, matching root_test.go's execRoot convention but
// scoped to just this one subcommand (statusDeps is not reachable from a
// full newRootCmd() tree, which always uses productionStatusDeps()).
func runStatusCmd(t *testing.T, deps statusDeps, jsonMode bool) (string, error) {
	t.Helper()
	cmd := newStatusCmd(deps)
	cmd.Flags().Bool("json", jsonMode, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &strings.Builder{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return buf.String(), err
}

// TestStatusCommand_HumanGolden asserts the default human-readable table
// against a golden fixture.
func TestStatusCommand_HumanGolden(t *testing.T) {
	deps, _ := startFakeStatusServer(t, fixedStatusResponse)
	got, err := runStatusCmd(t, deps, false)
	if err != nil {
		t.Fatalf("status: unexpected error: %v", err)
	}

	golden, err := os.ReadFile(filepath.Join("testdata", "golden_status_human.txt"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	if got != string(golden) {
		t.Errorf("human output mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
}

// TestStatusCommand_JSONEnvelope asserts --json emits internal/output's
// versioned envelope with the exact field names status.get's contract
// promises.
func TestStatusCommand_JSONEnvelope(t *testing.T) {
	deps, _ := startFakeStatusServer(t, fixedStatusResponse)
	got, err := runStatusCmd(t, deps, true)
	if err != nil {
		t.Fatalf("status --json: unexpected error: %v", err)
	}

	var envelope struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Data    struct {
			Version string `json:"version"`
			Daemon  struct {
				PID         int     `json:"pid"`
				UptimeS     float64 `json:"uptime_s"`
				Connections int     `json:"connections"`
				SocketPath  string  `json:"socket_path"`
			} `json:"daemon"`
			Health string `json:"health"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatalf("decode --json output: %v\noutput was:\n%s", err, got)
	}
	if envelope.Version != output.EnvelopeVersion {
		t.Errorf("envelope.version = %d, want %d", envelope.Version, output.EnvelopeVersion)
	}
	if !envelope.OK {
		t.Fatalf("envelope.ok = false, want true; output was:\n%s", got)
	}
	if envelope.Data.Version != fixedStatusResponse.Version {
		t.Errorf("data.version = %q, want %q", envelope.Data.Version, fixedStatusResponse.Version)
	}
	if envelope.Data.Daemon.PID != fixedStatusResponse.Daemon.PID {
		t.Errorf("data.daemon.pid = %d, want %d", envelope.Data.Daemon.PID, fixedStatusResponse.Daemon.PID)
	}
	if envelope.Data.Daemon.UptimeS != fixedStatusResponse.Daemon.UptimeS {
		t.Errorf("data.daemon.uptime_s = %v, want %v", envelope.Data.Daemon.UptimeS, fixedStatusResponse.Daemon.UptimeS)
	}
	if envelope.Data.Daemon.Connections != fixedStatusResponse.Daemon.Connections {
		t.Errorf("data.daemon.connections = %d, want %d", envelope.Data.Daemon.Connections, fixedStatusResponse.Daemon.Connections)
	}
	if envelope.Data.Daemon.SocketPath != fixedStatusResponse.Daemon.SocketPath {
		t.Errorf("data.daemon.socket_path = %q, want %q", envelope.Data.Daemon.SocketPath, fixedStatusResponse.Daemon.SocketPath)
	}
	if envelope.Data.Health != fixedStatusResponse.Health {
		t.Errorf("data.health = %q, want %q", envelope.Data.Health, fixedStatusResponse.Health)
	}
}

// TestStatusCommand_DaemonNotRunning asserts that calling status against a
// stopped daemon (a socket path nothing is listening on) returns a typed
// KindUnavailable error rather than a generic net.OpError or a panic.
func TestStatusCommand_DaemonNotRunning(t *testing.T) {
	root := t.TempDir()
	deps := statusDeps{
		Paths:   fakeDaemonPaths{root: root},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		DialContext: func(ctx context.Context, sockPath string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}

	_, err := runStatusCmd(t, deps, false)
	if err == nil {
		t.Fatal("status: expected an error against a stopped daemon, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestStatusCommand_StoppedAfterRunning proves the SAME command, called a
// second time after the daemon it just talked to stops, degrades from a
// real success to the same typed "not running" error - the exact
// AC-required "second invocation against a stopped daemon" case.
func TestStatusCommand_StoppedAfterRunning(t *testing.T) {
	deps, sockPath := startFakeStatusServer(t, fixedStatusResponse)

	if _, err := runStatusCmd(t, deps, false); err != nil {
		t.Fatalf("first status call: unexpected error: %v", err)
	}

	// Stop the daemon: remove the socket file, reproducing the "stale/gone
	// socket" shape a real stopped daemon leaves (net.Dial to a missing
	// path fails immediately). t.Cleanup already closes the server.
	_ = os.Remove(sockPath)

	_, err := runStatusCmd(t, deps, false)
	if err == nil {
		t.Fatal("second status call: expected an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestStatusCommand_NoInputHasNoEffect asserts automation parity (§5.8):
// CASCADE_NO_INPUT=1 never changes this read-only command's behavior,
// since it never prompts.
func TestStatusCommand_NoInputHasNoEffect(t *testing.T) {
	depsWithout, _ := startFakeStatusServer(t, fixedStatusResponse)
	gotWithout, errWithout := runStatusCmd(t, depsWithout, false)

	depsWith, _ := startFakeStatusServer(t, fixedStatusResponse)
	depsWith.Getenv = func(key string) string {
		if key == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	gotWith, errWith := runStatusCmd(t, depsWith, false)

	if (errWithout == nil) != (errWith == nil) {
		t.Fatalf("error presence differs: without=%v with=%v", errWithout, errWith)
	}
	if gotWithout != gotWith {
		t.Errorf("output differs under CASCADE_NO_INPUT=1:\nwithout:\n%s\nwith:\n%s", gotWithout, gotWith)
	}
}
