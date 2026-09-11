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
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_ConductorExpandReachableOverRealSocket proves
// conductor.expand is reachable on the daemon's own socket the same way
// TestBuildRPCServer_StatusReachableOverRealSocket (this package's
// sibling test) already proves for status.get: it starts the real server
// buildRPCServer constructs -- the same composition root platformDaemonRun
// calls in production -- serves it over a REAL unix socket, and dials
// conductor.expand from a REAL HTTP client. It never calls
// rpc.RegisterConductorExpand directly, which would prove only that the
// registrar function works, not that the daemon's own composition root
// ever calls it.
//
// The proof turns on the JSON-RPC error CODE, not just presence of an
// error: an unregistered method fails closed with codeMethodNotFound
// (-32601, internal/rpc/registry.go's methodNotFoundError). A registered
// conductor.expand instead reaches evidence.Fetcher.Expand, which fails
// on this claim id that was never written with evidence.ErrClaimNotFound
// (KindNotFound, pkg/cascade.RPCCodeNotFound = -32001). The two codes are
// disjoint by construction (jsonrpc.go's own doc comment on
// codeMethodNotFound), so seeing -32001 here is only possible if the
// composition root actually registered and dispatched to the real
// handler.
func TestBuildRPCServer_ConductorExpandReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)

	dir, err := os.MkdirTemp("", "expande2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	srv, manifest, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	if manifest == nil {
		t.Fatal("buildRPCServer returned a nil manifest")
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
		"id":      "expand-e2e",
		"method":  "conductor.expand",
		"params":  map[string]string{"claim_id": "CLM-does-not-exist"},
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("conductor.expand over the daemon socket returned no error for an unknown claim id, want ErrClaimNotFound")
	}
	const codeMethodNotFound = -32601
	const rpcCodeNotFound = -32001
	if envelope.Error.Code == codeMethodNotFound {
		t.Fatalf("conductor.expand -> %d %q: NOT REGISTERED on the real daemon composition root",
			envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.Error.Code != rpcCodeNotFound {
		t.Fatalf("Error.Code = %d %q, want %d (evidence.ErrClaimNotFound)",
			envelope.Error.Code, envelope.Error.Message, rpcCodeNotFound)
	}
}
