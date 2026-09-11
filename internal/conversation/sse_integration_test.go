//go:build !windows && integration

package conversation

// Purpose: the CLIENT-LOCAL ECHO integration proof this ticket's
//   acceptance criteria require: a REAL net/http SSE client, over a REAL
//   unix-socket listener, driven through the REAL internal/daemon.Run
//   entry point (not rpc.Handler called directly) -- matching
//   internal/daemon/daemon_ipc_e2e_integration_test.go's own precedent
//   for "the real thing, not a component in isolation." A JSON-RPC
//   chat.append_turn call is POSTed over the same socket, and the test
//   asserts the SSE conversation.turn_appended event's data arrives on
//   the stream BEFORE it reads that append's own JSON-RPC response body
//   -- CLIENT-LOCAL ECHO's literal ordering requirement.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). The
// ticket's files_scope.add lists sse_test.go for this file's kind of
// content, but sse_test.go already carries no build tag and must stay
// buildable in the default unit lane (06-FORGE-SPEC §5: the default lane
// forbids importing net, and this file imports net/net/http and requires
// a real unix socket). A real-socket test therefore cannot share a file
// with sse.go's portable RefuseSSEOnEmbedded unit tests; it needs its own
// `integration`-tagged file, exactly as internal/rpc's own
// handler_unix_integration_test.go and internal/daemon's
// daemon_ipc_e2e_integration_test.go are split out from their packages'
// untagged tests for the identical reason. This file is that split,
// named sse_integration_test.go to describe its content precisely.
//
// SPORT: internal.conversation.adapter/ADDED (integration test) (P1-E20-W5-S43-T2).

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

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// shortTempDirIT mirrors internal/daemon's own shortTempDir: a unix
// socket's sockaddr_un.sun_path is short enough (~104 bytes on darwin)
// that t.TempDir()'s long, test-name-embedding path can overflow it.
func shortTempDirIT(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "convit")
	if err != nil {
		t.Fatalf("shortTempDirIT: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func unixHTTPClientIT(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// TestClientLocalEcho_RealSocket_EchoPrecedesResponse is CLIENT-LOCAL
// ECHO's real-counterpart proof (Art.2): the SSE event for an appended
// turn must be observable on a subscribed real SSE stream before this
// test reads the JSON-RPC response confirming that same append.
func TestClientLocalEcho_RealSocket_EchoPrecedesResponse(t *testing.T) {
	dir := shortTempDirIT(t)
	socketPath := filepath.Join(dir, "conv.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	clock := runtime.NewSystemClock()

	bus := events.New(storetest.NewMemStore(), clock)
	store := newTestStore(t)
	adapter := NewAdapter(store, bus, passthroughSubst{}, clock, "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)

	known := func(kind events.EventKind) bool { return kind == turnAppendedKind }
	srv := daemon.NewRPCServer(registry, rpc.NewSSEHandler(bus, turnAppendedNamespace, known, clock))

	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(context.Background(), daemon.RunOptions{
			Settings: daemon.Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
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
		t.Fatalf("daemon.Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon.Run never became ready")
	}
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("daemon.Run did not return after a termination signal")
		}
	})

	client := unixHTTPClientIT(socketPath)

	sseResp, err := client.Get("http://unix" + rpc.EventsPath)
	if err != nil {
		t.Fatalf("GET %s over the real socket: %v", rpc.EventsPath, err)
	}
	t.Cleanup(func() { _ = sseResp.Body.Close() })
	reader := bufio.NewReader(sseResp.Body)

	echoArrived := make(chan struct{})
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			line, rerr := reader.ReadString('\n')
			if rerr != nil {
				return
			}
			// Any "data:" line on this stream is this test's single
			// subscribed conversation.turn_appended event -- nothing
			// else publishes to this namespace in this test.
			if strings.HasPrefix(line, "data:") {
				close(echoArrived)
				return
			}
		}
	}()

	body := []byte(`{"jsonrpc":"2.0","method":"chat.append_turn","id":"1","params":{"thread_id":"th1","role":"user","segments":[{"kind":"text","content":"hi"}]}}`)
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	// This is the ordering assertion: block on the SSE echo BEFORE
	// decoding the RPC response body, with a bounded select (never a
	// bare <-ch) so a real regression hangs the test with a clear
	// message instead of forever.
	select {
	case <-echoArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("SSE echo for the appended turn never arrived within 10s")
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode JSON-RPC response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("chat.append_turn JSON-RPC error: %+v", envelope.Error)
	}
}
