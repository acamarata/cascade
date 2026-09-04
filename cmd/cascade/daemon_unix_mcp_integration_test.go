//go:build !windows && integration

package main

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
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/rpc"
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

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: "mcpe2e.sock"}, fakeMemoryPaths{root: t.TempDir()})
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}

	dir, err := os.MkdirTemp("", "mcpe2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

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

	frame, err := json.Marshal(map[string]any{"mcp_method": "tools/list", "mcp_name": ""})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  transport.MCPMethod,
		"id":      "mcp-1",
		"params":  json.RawMessage(frame),
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
	if envelope.Error != nil {
		t.Fatalf("mcp dispatch over the daemon socket returned an error: %d %s",
			envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		t.Fatal("mcp dispatch resolved but returned nothing")
	}
}
