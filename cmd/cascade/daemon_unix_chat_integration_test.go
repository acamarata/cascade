//go:build !windows && integration

package main

// Purpose (this file): proof that chat.* is reachable on the daemon's own
//   socket — the thing that was missing, not the adapter, which was built
//   and tested throughout (R-14.284).
// SPORT: cmd/cascade daemon chat wiring (ADD) — P1-E20-W5-S43-T5.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_ChatReachableOverRealSocket starts the real server
// buildRPCServer constructs — the same composition root platformDaemonRun
// calls in production — serves it over a REAL unix socket, and appends a
// turn through a REAL HTTP client.
//
// It never calls Adapter.RegisterHandlers directly. That would prove the
// registrar works, which was never in doubt: `internal/conversation` had
// a full adapter, a full store and a passing suite, and `cascade chat`
// still answered "method not found" from a real daemon because nothing
// called it. Only a test that goes through buildRPCServer can tell the
// difference.
func TestBuildRPCServer_ChatReachableOverRealSocket(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "d.sock")

	srv, _, _, err := buildRPCServer(bus, clock, nil,
		daemon.Settings{SocketPath: sockPath}, fakeMemoryPaths{root: t.TempDir()}, nil, nil)
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

	// A real append, not a probe for the method's existence. The turn has
	// to land: a handler registered over a store that cannot write would
	// also answer something other than method-not-found.
	result := chatCall(t, client, "chat.append_turn", map[string]any{
		"thread_id": "wiring-e2e",
		"role":      "user",
		"segments":  []map[string]string{{"kind": "text", "content": "hello"}},
	})
	turnID, _ := result["turn_id"].(string)
	if turnID == "" {
		t.Fatalf("chat.append_turn returned no turn id: %v", result)
	}

	// And it is readable back through a SECOND method, so the store the
	// write went to is the store the reads come from.
	thread := chatCall(t, client, "chat.get_thread", map[string]any{"thread_id": "wiring-e2e"})
	turns, _ := thread["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("chat.get_thread returned %d turn(s) after one append: %v", len(turns), thread)
	}

	listed := chatCall(t, client, "chat.list_threads", map[string]any{})
	threads, _ := listed["threads"].([]any)
	if len(threads) == 0 {
		t.Fatalf("chat.list_threads reports no threads after one was written: %v", listed)
	}
}

// chatCall posts one JSON-RPC request and fails on any error, naming
// method-not-found specifically — that code is the regression this file
// exists to catch.
func chatCall(t *testing.T, client *http.Client, method string, params map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": method, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var envelope struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	if envelope.Error != nil {
		const codeMethodNotFound = -32601
		if envelope.Error.Code == codeMethodNotFound {
			t.Fatalf("%s: NOT REGISTERED on the real daemon composition root (%q)", method, envelope.Error.Message)
		}
		t.Fatalf("%s -> %d %q", method, envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result
}
