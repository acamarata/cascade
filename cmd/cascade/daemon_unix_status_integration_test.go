//go:build !windows && integration

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_StatusReachableOverRealSocket proves status.get is
// reachable on the daemon's own socket in the exact same way
// TestBuildRPCServer_MCPReachableOverRealSocket (this package's sibling
// test) already proves for the MCP dispatcher: it starts the real server
// buildRPCServer constructs, serves it over a REAL unix socket, and dials
// status.get from a REAL HTTP client - not a direct in-process call to the
// handler function, which would prove nothing about whether the
// composition root actually registered it (this ticket's contract calls
// this distinction out explicitly: "a test that calls the handler function
// directly does NOT satisfy this and does not close the gap").
//
// It also asserts the response is REAL, live data, not a placeholder: PID
// matches this test process's own pid (status.get always runs inside the
// daemon process, and in this test the "daemon" IS the test binary), and
// Health is "ok" because buildRPCServer registers no failing subsystem.
func TestBuildRPCServer_StatusReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	dir := mkdirTempIT(t, "statuse2e")
	sockPath := filepath.Join(dir, "d.sock")

	srv, manifest, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	if manifest == nil {
		t.Fatal("buildRPCServer returned a nil manifest")
	}

	serveOnSocketIT(t, srv, sockPath)

	client := realSocketHTTPClientIT(sockPath)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "status-e2e",
		"method":  daemon.StatusMethod,
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result daemon.StatusResponse `json:"result"`
	}
	postRPCIT(t, client, req, &envelope)
	if envelope.Error != nil {
		t.Fatalf("status.get over the daemon socket returned an error: %d %s",
			envelope.Error.Code, envelope.Error.Message)
	}

	if envelope.Result.Daemon.PID != os.Getpid() {
		t.Errorf("Daemon.PID = %d, want this test process's pid %d", envelope.Result.Daemon.PID, os.Getpid())
	}
	if envelope.Result.Daemon.SocketPath != sockPath {
		t.Errorf("Daemon.SocketPath = %q, want %q", envelope.Result.Daemon.SocketPath, sockPath)
	}
	if envelope.Result.Health != "ok" {
		t.Errorf("Health = %q, want %q", envelope.Result.Health, "ok")
	}
	if envelope.Result.Version == "" {
		t.Error("Version is empty")
	}
}
