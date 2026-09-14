//go:build !windows && integration

package main

// Purpose: proves "plugin.add" is reachable on the daemon's REAL socket
// through the REAL composition root (buildRPCServer ->
// daemon.RegisterPluginHandler -> internal/plugins.ProvisionElevated),
// closing the R-16.80 gate TestRPCMethodGate_RealTreeGreen found:
// cmd/cascade/plugin_add.go's elevated leg dials "plugin.add" and, before
// this ticket's COMPLETION PASS, nothing in the tracked tree registered
// it. Mirrors daemon_unix_journal_integration_test.go's own pattern: a
// real unix-socket HTTP round trip, never calling the handler function
// in-process (which would prove nothing about whether the composition
// root actually registered it).
//
// A process-tier manifest is the deliberate fixture: §5.14 always
// elevates it, and no path in this tree ever marks a manifest's
// process.TrustTier above TrustTierUntrusted, so ProvisionElevated's
// process branch always refuses — the real, typed, wrapUntrusted
// message, not a fabricated install and not "method not found".

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// processManifestSourceE2E mirrors internal/plugins/lifecycle_add_test.go's
// own processManifest fixture exactly (a real, ParseManifest-valid
// cascade.plugin/v2 document is required over the wire — the daemon-side
// handler re-parses the raw bytes, it does not accept a Go struct).
const processManifestSourceE2E = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
`

func TestBuildRPCServer_PluginAddReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	store := storetest.NewMemStore()

	dir, err := os.MkdirTemp("", "pluginaddE2E")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	sockPath := filepath.Join(dir, "d.sock")

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: dir}, nil, store)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

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

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "plugin-add-e2e",
		"method":  "plugin.add",
		"params": map[string]any{
			"id":             "demo",
			"manifest_bytes": []byte(processManifestSourceE2E),
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s -> %d, want 200", rpc.RPCPath, resp.StatusCode)
	}

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// The real, live composition root must answer with the real
	// process-tier refusal, not "method not found" (proves registration)
	// and not a fabricated install (proves ProvisionElevated's process
	// branch, and process.TrustTierUntrusted, actually ran).
	if envelope.Error == nil {
		t.Fatalf("plugin.add over the daemon socket succeeded for a process-tier manifest with no trust "+
			"mechanism in this tree; want a refusal, got result %s", envelope.Result)
	}
	if envelope.Error.Message == "" {
		t.Fatalf("plugin.add error message is empty")
	}
	const wantSubstr = "trust_tier"
	if !bytes.Contains([]byte(envelope.Error.Message), []byte(wantSubstr)) {
		t.Fatalf("plugin.add error = %q, want it to contain %q (the real process.wrapUntrusted message, "+
			"not a generic method-not-found)", envelope.Error.Message, wantSubstr)
	}
}
