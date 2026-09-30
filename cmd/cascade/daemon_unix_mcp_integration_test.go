//go:build !windows && integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_MCPReachableOverRealSocket proves the MCP dispatcher is
// reachable on the daemon's own socket, not only on the separate socket the
// mcp command binds for itself.
//
// The transport was complete and tested long before it was reachable here.
// That is precisely why this test serves the real server over a real unix
// socket and dials it: asking the transport whether it works passed the
// whole time it was unreachable. Driving the handler in-process does not
// substitute either, because the daemon refuses a request carrying no
// socket-peer credentials, which is the ownership check doing its job.
func TestBuildRPCServer_MCPReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: "mcpe2e.sock"}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	dir, err := os.MkdirTemp("", "mcpe2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	serveOnSocketIT(t, srv, sockPath)

	client := realSocketHTTPClientIT(sockPath)

	frame, err := json.Marshal(map[string]any{"mcp_method": "tools/list", "mcp_name": ""})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  transport.MCPMethod,
		"id":      "mcp-1",
		"params":  json.RawMessage(frame),
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	postRPCIT(t, client, req, &envelope)
	if envelope.Error != nil {
		t.Fatalf("mcp dispatch over the daemon socket returned an error: %d %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		t.Fatal("mcp dispatch resolved but returned nothing")
	}
}
