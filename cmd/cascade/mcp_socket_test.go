//go:build !windows && integration

// Purpose: proves `cascade mcp serve --socket` (serveSocketReal, mcp.go)
//   runs behind the SAME local request guard (internal/rpc/request_guard.go,
//   P1-E04-W6-S146-T1) the daemon's own socket does, now that
//   ConnContext: rpc.ConnContext is wired on its http.Server — before this
//   ticket, serveSocketReal built no ConnContext at all, so every request's
//   peerCred resolved ok=false and guardLocalRequest (and the inline check
//   it replaced) refused every request unconditionally, fail-closed but
//   non-functional for the socket's own owner.
// Constraints: binds a REAL unix socket, so this file carries //go:build
//   !windows && integration, matching every other real-unix-socket
//   integration test in this package (e.g. daemon_unix_socket_integration_test.go)
//   — the unix-socket transport this test drives has no Windows
//   equivalent, and internal/build's no-network-unit-lane gate (Art.7.2)
//   forbids an untagged _test.go file from importing "net"/"net/http"
//   in the first place.
//
// SPORT: cmd/cascade/mcp request_guard wiring [ADD] (P1-E04-W6-S146-T1).

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
)

// mcpSocketTestPaths builds a short-root PathProvider and tool registry for
// startMCPSocketServer, returning the socket path serveSocketReal will
// bind. A short os.MkdirTemp root, NOT t.TempDir(): t.TempDir() embeds the
// full test name in the path, which on darwin's 104-byte sockaddr_un
// limit (client.go's own maxUnixSocketPathBytes) overflows well before
// this socket's own "-mcp" suffix is appended, and net.Listen then fails
// silently into this helper's caller with no socket file ever created —
// client_integration_test.go's startRealDaemonSocket uses the same
// short-root pattern for the same reason. Split out of
// startMCPSocketServer to keep both functions under the 50-line cap
// (Art.10.3).
func mcpSocketTestPaths(t *testing.T) (paths runtime.PathProvider, tools *mcp.ToolRegistry, sockPath string) {
	t.Helper()
	root, err := os.MkdirTemp("", "mcpsock")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	paths, err = runtime.NewPathProvider(func(key string) string {
		if key == "CASCADE_HOME" {
			return root
		}
		return ""
	}, func() (string, error) { return root, nil })
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	tools = mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{})
	return paths, tools, paths.SocketPath() + "-mcp"
}

// startMCPSocketServer runs serveSocketReal against mcpSocketTestPaths'
// short-root PathProvider, waits for the "-mcp"-suffixed socket file to
// appear, and returns an http.Client dialing it plus that socket's path.
// t.Cleanup cancels the serving context and waits for serveSocketReal to
// return.
func startMCPSocketServer(t *testing.T) (client *http.Client, sockPath string) {
	t.Helper()
	paths, tools, sockPath := mcpSocketTestPaths(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if serveErr := serveSocketReal(ctx, paths, tools); serveErr != nil && ctx.Err() == nil {
			t.Logf("serveSocketReal: %v", serveErr)
		}
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serveSocketReal did not return after cancel")
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Fatalf("mcp socket never appeared at %s: %v", sockPath, statErr)
	}

	client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	return client, sockPath
}

// mcpDispatchRequest is a well-formed JSON-RPC envelope naming the socket's
// one registered method (transport.MCPMethod, "mcp.dispatch") — this test
// only needs the request to clear the local request guard and reach
// Registry.Dispatch; what mcp.dispatch itself does with an empty params
// object is transport's own concern, not this ticket's.
const mcpDispatchRequest = `{"jsonrpc":"2.0","method":"mcp.dispatch","id":1,"params":{}}`

// TestMCPSocketServeAcceptsOwnerPeer proves an owner-UID client (Host
// "unix", Content-Type application/json, no Origin/Sec-Fetch headers —
// this test process's own shape) is served, not refused, now that
// ConnContext resolves its peer credential.
func TestMCPSocketServeAcceptsOwnerPeer(t *testing.T) {
	client, _ := startMCPSocketServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/rpc", bytes.NewReader([]byte(mcpDispatchRequest)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the owner peer", resp.StatusCode)
	}
}

// TestMCPSocketServeRefusesOriginHeader proves the same browser-shaped
// refusal request_guard.go applies to the daemon socket also applies here
// — an Origin header refuses with 403 before mcp.dispatch ever runs, the
// same guard, not a laxer one. Asserting the exact browser-shaped body
// (not just != 403 from the owner-UID branch, and not "contains one of
// the two fixed strings") is what makes this test fail if ConnContext
// were ever removed from serveSocketReal's http.Server: with no
// ConnContext, every peerCred resolves ok=false and the owner-UID branch
// refuses first, with its own different body, before browserShaped ever
// runs — see this ticket's mutation 3.
func TestMCPSocketServeRefusesOriginHeader(t *testing.T) {
	client, _ := startMCPSocketServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://unix/rpc", bytes.NewReader([]byte(mcpDispatchRequest)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an Origin-carrying request", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	const wantBody = "forbidden: browser-shaped request refused"
	if got := strings.TrimSpace(string(body)); got != wantBody {
		t.Fatalf("body = %q, want %q (the owner-UID branch's body would mean ConnContext never resolved a peer)", got, wantBody)
	}
}
