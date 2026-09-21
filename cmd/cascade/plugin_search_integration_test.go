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
		"id":      "plugin-search-e2e",
		"method":  "plugin.search",
		"params":  map[string]any{"q": ""},
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
		Result []map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

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
