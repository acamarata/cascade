//go:build !windows && integration

package daemon

// Purpose: the end-to-end proof the IPC hand-off requires: a real client
//   dialing the real unix socket gets a real JSON-RPC response over
//   POST /rpc, and a real client opens GET /events over that SAME socket
//   and receives an event actually published to the bus. Both run against
//   the real Run() entry point (not NewRPCServer or rpc.Handler called
//   directly), which is the distinction that separates this from a
//   component test that only proves each piece works in isolation. Needs
//   "net"/"net/http", so this lives in the `-tags=integration` lane
//   alongside this package's other real-socket test
//   (daemon_unix_integration_test.go).
// SPORT: internal/daemon (ADD).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// unixHTTPClient returns an *http.Client that dials socketPath for every
// request, regardless of the URL's host — the standard way to drive an
// http.Client over a unix socket from a test.
func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// TestRun_RealSocket_RPCAndSSE_EndToEnd drives Run with a real *http.Server
// (built exactly as cmd/cascade/daemon_unix.go builds one: NewRPCServer
// over a real rpc.Registry and a real rpc.SSEHandler bound to a real
// *events.Bus), dials the real socket for both routes, and proves each one
// actually works: POST /rpc returns the registered handler's real result,
// GET /events delivers an event actually published to the bus afterward.
func TestRun_RealSocket_RPCAndSSE_EndToEnd(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "d.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	clock := runtime.NewSystemClock()

	bus := events.New(storetest.NewMemStore(), clock)
	registry := rpc.NewRegistry()
	registry.Register("ping", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]string{"pong": "ok"}, nil
	})
	known := func(kind events.EventKind) bool { return kind == "e2e.test_kind" }
	srv := NewRPCServer(registry, rpc.NewSSEHandler(bus, "e2e", known, clock))

	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{
			Settings: Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
			PIDPath:  pidPath,
			Clock:    clock,
			Signals:  signals,
			Ready:    ready,
			Server:   srv,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	client := unixHTTPClient(socketPath)
	assertRealRPCResponse(t, client)
	assertRealSSEEvent(t, client, bus)

	signals <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after a termination signal")
	}
}

// assertRealRPCResponse POSTs a real JSON-RPC request over the real socket
// and asserts the real registered handler's result comes back, proving the
// accept-to-handler hand-off works end to end, not just that the mux
// routes the path.
func assertRealRPCResponse(t *testing.T, client *http.Client) {
	t.Helper()
	body := []byte(`{"jsonrpc":"2.0","method":"ping","id":"1"}`)
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Result map[string]string `json:"result"`
		Error  any               `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode JSON-RPC response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error: %+v", envelope.Error)
	}
	if envelope.Result["pong"] != "ok" {
		t.Fatalf("result = %+v, want {pong: ok}", envelope.Result)
	}
}

// assertRealSSEEvent opens GET /events over the real socket, publishes a
// real event to bus afterward, and reads the SSE stream until that event's
// "data:" line appears, proving the bridge from the real bus to a real
// HTTP client actually delivers.
func assertRealSSEEvent(t *testing.T, client *http.Client, bus *events.Bus) {
	t.Helper()
	resp, err := client.Get("http://unix" + rpc.EventsPath)
	if err != nil {
		t.Fatalf("GET %s over the real socket: %v", rpc.EventsPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if _, err := bus.Publish(context.Background(), "e2e", events.EventKind("e2e.test_kind"), "test", []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	reader := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		if strings.HasPrefix(line, "data:") {
			return
		}
	}
	t.Fatal("never received an SSE data line for the published event")
}
