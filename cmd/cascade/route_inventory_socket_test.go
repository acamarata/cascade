//go:build !windows

// Purpose: the unix-socket transport and server starters behind
//   route_inventory_test.go: one raw HTTP/1.1 request per connection over
//   the first-party client.UnixDialer, and the real mcp and node serve
//   entry points run until the test ends. Split out under the 300-line cap.
// Inputs: a socket path; header overrides on a first-party request.
// Outputs: the response status and its Content-Length-bounded body.
// Constraints: imports neither "net" nor "net/http" (the no-network
//   unit-test gate); every socket lives under a short temp dir (sun_path).
// SPORT: cmd/cascade route inventory transport [ADD].

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
)

// socketTrip dials sock with the first-party client.UnixDialer and writes
// one raw HTTP/1.1 request, so the server's ConnContext sees this process
// as a real peer.
func socketTrip(sock string) roundTrip {
	return func(t *testing.T, method, target string, header map[string]string) (int, string) {
		t.Helper()
		conn, err := client.UnixDialer(context.Background(), sock)
		if err != nil {
			t.Fatalf("dial %s: %v", sock, err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, rawRequest(method, target, header)); err != nil {
			t.Fatalf("write %s %s: %v", method, target, err)
		}
		return readResponse(t, bufio.NewReader(conn))
	}
}

// rawRequest renders a first-party owner-CLI request (Host "unix", the
// owner marker, a JSON body on POST) with header's entries replacing or adding header lines.
func rawRequest(method, target string, header map[string]string) string {
	h := map[string]string{"Host": "unix", "Connection": "close", principal.HeaderOwnerCLI: "owner-cli"}
	body := ""
	if method == "POST" {
		body = `{"jsonrpc":"2.0","id":1,"method":"route.inventory.probe"}`
		h["Content-Type"], h["Content-Length"] = "application/json", strconv.Itoa(len(body))
	}
	for k, v := range header {
		h[k] = v
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", method, target)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\r\n", k, h[k])
	}
	b.WriteString("\r\n" + body)
	return b.String()
}

// readResponse parses the status line and headers, and reads the body only
// when a Content-Length bounds it (an SSE stream has none).
func readResponse(t *testing.T, br *bufio.Reader) (int, string) {
	t.Helper()
	line, err := br.ReadString('\n')
	fields := strings.Fields(line)
	if err != nil || len(fields) < 2 {
		t.Fatalf("status line %q: %v", line, err)
	}
	code, _ := strconv.Atoi(fields[1])
	length := -1
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("response headers: %v", err)
		}
		if l = strings.TrimRight(l, "\r\n"); l == "" {
			break
		}
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if length < 0 {
		return code, ""
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("response body: %v", err)
	}
	return code, string(body)
}

// serveMCPSocket runs the real `cascade mcp serve --socket` path over a
// fresh CASCADE_HOME and returns its socket.
func serveMCPSocket(t *testing.T) string {
	t.Helper()
	root := shortTempDir(t)
	paths, err := runtime.NewPathProvider(func(key string) string {
		if key == "CASCADE_HOME" {
			return root
		}
		return ""
	}, func() (string, error) { return root, nil })
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	tools := mcp.NewToolRegistry(plugin.Builtins, mcp.AllowAllFilter{})
	sock := paths.SocketPath() + "-mcp"
	serveUntilCleanup(t, sock, func(ctx context.Context) error { return serveSocketReal(ctx, paths, tools) })
	return sock
}

// serveNodeSocket runs the real node serve entry point and returns its
// socket.
func serveNodeSocket(t *testing.T) string {
	t.Helper()
	deps := testNodeServeDeps(t)
	sock := filepath.Join(deps.Paths.DataDir(), "nodes", "node.sock")
	serveUntilCleanup(t, sock, func(ctx context.Context) error { return runNodeServe(ctx, deps) })
	return sock
}

// serveUntilCleanup runs serve until the test ends (then cancels it and
// waits for it to return) and blocks until sock accepts a connection (the
// socket file exists between bind and listen, so a stat is not enough).
func serveUntilCleanup(t *testing.T, sock string, serve func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var serveErr error
	done := make(chan struct{})
	go func() { serveErr = serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not return after cancel")
		}
	})
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for {
		if conn, err := client.UnixDialer(ctx, sock); err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-done:
			t.Fatalf("server returned before binding %s: %v", sock, serveErr)
		case <-deadline:
			t.Fatalf("socket %s never appeared", sock)
		case <-tick.C:
		}
	}
}
