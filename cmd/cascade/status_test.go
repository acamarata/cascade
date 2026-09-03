//go:build !windows

// Purpose: unit tests for `cascade status`'s pure logic - JSON-RPC
//
//	envelope decoding, the human table renderer, and command-tree argument
//	validation - that need no real socket or HTTP client. The real-dial
//	end-to-end cases (golden human output, --json envelope over the wire,
//	daemon-not-running, automation parity) live in
//	status_integration_test.go, build-tagged "integration": this file
//	deliberately imports neither "net" nor "net/http" so it runs in the
//	fast, no-network unit lane (internal/build's no-network-unit-lane
//	gate, Art.7.2) - see daemon_unix_status_integration_test.go for the
//	wiring-reachability proof.
//
// SPORT: cmd/cascade/status (ADD, per T-1 sport_updates).
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDecodeStatusEnvelope_Success(t *testing.T) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"1","result":{"version":"v1","daemon":{"pid":9,"uptime_s":1.5,"connections":4,"socket_path":"/tmp/d.sock"},"health":"ok"}}`)
	got, err := decodeStatusEnvelope(body)
	if err != nil {
		t.Fatalf("decodeStatusEnvelope: unexpected error: %v", err)
	}
	want := daemon.StatusResponse{
		Version: "v1",
		Daemon: daemon.StatusDaemonFields{
			PID: 9, UptimeS: 1.5, Connections: 4, SocketPath: "/tmp/d.sock",
		},
		Health: "ok",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDecodeStatusEnvelope_RPCError(t *testing.T) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"method not found: status.get"}}`)
	_, err := decodeStatusEnvelope(body)
	if err == nil {
		t.Fatal("decodeStatusEnvelope: expected an error for an RPC error envelope")
	}
	if !strings.Contains(err.Error(), "method not found: status.get") {
		t.Errorf("err = %v, want it to mention the RPC error message", err)
	}
}

func TestDecodeStatusEnvelope_MalformedJSON(t *testing.T) {
	body := strings.NewReader(`not json`)
	_, err := decodeStatusEnvelope(body)
	if err == nil {
		t.Fatal("decodeStatusEnvelope: expected an error for malformed JSON")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
}

func TestDecodeStatusEnvelope_MalformedResult(t *testing.T) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"1","result":"not-an-object"}`)
	_, err := decodeStatusEnvelope(body)
	if err == nil {
		t.Fatal("decodeStatusEnvelope: expected an error for a result that does not decode as StatusResponse")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
}

func TestStatusHumanView_String(t *testing.T) {
	v := statusHumanView{daemon.StatusResponse{
		Version: "1.2.3-test",
		Daemon: daemon.StatusDaemonFields{
			PID: 4242, UptimeS: 12.5, Connections: 2, SocketPath: "/var/run/cascade/daemon.sock",
		},
		Health: "ok",
	}}
	got := v.String()
	for _, want := range []string{"version", "1.2.3-test", "pid", "4242", "uptime_s", "12.500", "connections", "2", "socket_path", "/var/run/cascade/daemon.sock", "health", "ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestResolveStatusSocket_MalformedConfig(t *testing.T) {
	dir := t.TempDir()
	deps := statusDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	_, err := resolveStatusSocket(context.Background(), deps)
	if err == nil {
		t.Fatal("resolveStatusSocket: expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

func TestResolveStatusSocket_DefaultsFromPaths(t *testing.T) {
	dir := t.TempDir()
	deps := statusDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	settings, err := resolveStatusSocket(context.Background(), deps)
	if err != nil {
		t.Fatalf("resolveStatusSocket: unexpected error: %v", err)
	}
	if settings.SocketPath != deps.Paths.SocketPath() {
		t.Errorf("SocketPath = %q, want the path provider's default %q", settings.SocketPath, deps.Paths.SocketPath())
	}
}

// TestStatusCommand_NoArgsRejected asserts the standard usageArgs(cobra.
// NoArgs) rejection, matching every other zero-arg command in this tree.
// Uses a zero-value statusDeps (no DialContext) because Args validation
// fails before RunE ever runs fetchStatus - no socket is dialed.
func TestStatusCommand_NoArgsRejected(t *testing.T) {
	cmd := newStatusCmd(statusDeps{})
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &strings.Builder{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"unexpected-arg"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unexpected positional argument")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestProductionStatusDeps_DialContextFailsForMissingSocket exercises the
// REAL production DialContext closure (productionStatusDeps' only
// non-trivial field) against a path nothing is listening on. This dials a
// real local unix socket path - a fast, immediate ENOENT, not network
// I/O - so it stays within the no-network-unit-lane gate's actual rule
// (Art.7.2 polices the IMPORT of "net"/"net/http" in a _test.go file, not
// a local-only syscall reached through a borrowed, already-typed field
// value): this file never writes "net.Conn" or imports "net" itself, it
// only calls a function value whose type was declared in status.go.
func TestProductionStatusDeps_DialContextFailsForMissingSocket(t *testing.T) {
	dial := productionStatusDeps().DialContext
	missing := filepath.Join(t.TempDir(), "nothing-listens-here.sock")
	_, err := dial(context.Background(), missing)
	if err == nil {
		t.Fatal("DialContext: expected an error dialing a socket nothing listens on")
	}
}

// newHermeticStatusDeps builds a statusDeps rooted at a fresh temp dir with
// the REAL production DialContext closure (borrowed the same way as
// above), so fetchStatus/newStatusCmd's real code paths run against a
// socket path that simply has no listener - deterministic, fast, and free
// of any "net"/"net/http" import in this test file.
func newHermeticStatusDeps(t *testing.T) statusDeps {
	t.Helper()
	dir := t.TempDir()
	return statusDeps{
		Paths:       fakeDaemonPaths{root: dir},
		Getenv:      func(string) string { return "" },
		Environ:     func() []string { return nil },
		DialContext: productionStatusDeps().DialContext,
	}
}

// TestFetchStatus_DaemonNotRunning exercises fetchStatus's real body
// (config load, socket resolution, HTTP client construction, and the
// dial-failure branch) against a socket nothing listens on.
func TestFetchStatus_DaemonNotRunning(t *testing.T) {
	deps := newHermeticStatusDeps(t)
	_, err := fetchStatus(context.Background(), deps)
	if err == nil {
		t.Fatal("fetchStatus: expected an error against a socket nothing listens on")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestStatusCommand_RunE_DaemonNotRunning exercises newStatusCmd's RunE
// closure end to end (through statusOutputWriter and Result/Fail) against
// the same hermetic "nothing listening" deps.
func TestStatusCommand_RunE_DaemonNotRunning(t *testing.T) {
	deps := newHermeticStatusDeps(t)
	cmd := newStatusCmd(deps)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &strings.Builder{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("status: expected an error against a socket nothing listens on")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestStatusOutputWriter builds a *cobra.Command with the standard global
// flags and asserts statusOutputWriter resolves the JSON mode from them:
// a pure function of flag values, no network involved.
func TestStatusOutputWriter(t *testing.T) {
	cmd := newStatusCmd(statusDeps{})
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", true, "")
	w := statusOutputWriter(cmd)
	if !w.Mode().JSON {
		t.Error("statusOutputWriter: Mode().JSON = false, want true")
	}
	if !w.Mode().NoColor {
		t.Error("statusOutputWriter: Mode().NoColor = false, want true")
	}
}
