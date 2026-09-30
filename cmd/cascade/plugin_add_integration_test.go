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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
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

	dir := mkdirTempIT(t, "pluginaddE2E")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	sockPath := filepath.Join(dir, "d.sock")

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: dir}, nil, store)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	serveOnSocketIT(t, srv, sockPath)

	client := realSocketHTTPClientIT(sockPath)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "plugin-add-e2e",
		"method":  "plugin.add",
		"params": map[string]any{
			"id":             "demo",
			"manifest_bytes": []byte(processManifestSourceE2E),
		},
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	postRPCIT(t, client, req, &envelope)

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
