//go:build !windows && integration

package main

// Purpose: proves "plugin.search" is reachable on the daemon's REAL
// socket through the REAL composition root (buildRPCServer ->
// registerDBPathHandlers -> wirePluginSearchHandler), mirroring
// plugin_add_integration_test.go's own pattern exactly. This is the test
// the adversarial review's mutation targets (s50t2-cr-verdict.txt finding
// 3): removing the `wirePluginSearchHandler(ctx, registry, paths, clock)`
// call from plugin_rpc.go's registerDBPathHandlers makes THIS test fail
// with "method not found" — nothing else in the suite dials "plugin.search"
// through the real daemon socket (TestPluginSearchRPCWiring, in
// plugin_search_catalog_test.go, calls wirePluginSearchHandler directly,
// which proves the handler itself is real but not that the daemon's own
// composition root actually wires it).
//
// A real unix-socket HTTP round trip, never calling the handler function
// in-process — Art.7.2 excludes this from the unit lane (hence the
// `integration` build tag), the same exclusion plugin_add_integration_
// test.go's own header already states for the identical reason.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

func TestBuildRPCServer_PluginSearchReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	store := storetest.NewMemStore()

	dir, err := os.MkdirTemp("", "pluginsearchE2E")
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

	serveOnSocketIT(t, srv, sockPath)

	client := realSocketHTTPClientIT(sockPath)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "plugin-search-e2e",
		"method":  "plugin.search",
		"params":  map[string]any{"q": ""},
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result []map[string]any `json:"result"`
	}
	postRPCIT(t, client, req, &envelope)

	// The real, live composition root must answer with the real catalog,
	// not "method not found" (proves registerDBPathHandlers' call to
	// wirePluginSearchHandler actually ran).
	if envelope.Error != nil {
		t.Fatalf("plugin.search over the real daemon socket returned an error: %+v (want the builtin catalog)", envelope.Error)
	}
	if len(envelope.Result) == 0 {
		t.Fatal("plugin.search over the real daemon socket returned zero entries; want at least the builtin catalog")
	}
}
