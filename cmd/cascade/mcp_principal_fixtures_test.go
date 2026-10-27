//go:build !windows

// Purpose: shared fixtures for the MCP caller-principal tests
//   (mcp_scoped_forward_test.go, mcp_socket_principal_test.go): handler
//   recorders, a scratch builtin plugin, fake daemons on real unix sockets,
//   raw HTTP/1.1 posts over client.UnixDialer and a stdio exchange over an
//   io.Pipe. Split out under the 300-line cap.
// Inputs: short temp dirs (sun_path); obviously fake tokens and principals.
// Outputs: helpers only.
// Constraints: imports neither "net" nor "net/http" (the no-network
//   unit-test gate).
// SPORT: cmd/cascade/mcp principal mode tests [ADD].

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httputil"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
)

func callFrame(name string) string {
	return `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
}

type seenCaller struct {
	method string
	caller principal.Caller
	ok     bool
}

// recorder counts handler runs and the Caller each one saw.
type recorder struct {
	name string
	mu   sync.Mutex
	seen []seenCaller
}

func (r *recorder) handler(method string) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		r.note(ctx, method)
		return map[string]string{"answered_by": r.name, "method": method}, nil
	}
}

func (r *recorder) note(ctx context.Context, method string) {
	c, ok := principal.CallerFrom(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, seenCaller{method, c, ok})
}

func (r *recorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.seen {
		if method == "" || s.method == method {
			n++
		}
	}
	return n
}

func (r *recorder) all() []seenCaller {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenCaller(nil), r.seen...)
}

// probeHandlers is the scratch builtin plugin: it records the Caller.
type probeHandlers struct{ rec *recorder }

func (p probeHandlers) DispatchTool(ctx context.Context, name string, _ []byte) ([]byte, error) {
	p.rec.note(ctx, name)
	return []byte(`"` + p.rec.name + `"`), nil
}
func (probeHandlers) DispatchIntent(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (probeHandlers) RunCommand(context.Context, string, []string) error             { return nil }

// scratchSource is plugin.Builtins plus one scratch registration (never
// plugin.RegisterBuiltin, whose registry freezes on first use).
func scratchSource(rec *recorder) mcp.ManifestSource {
	return func() []plugin.BuiltinRegistration {
		return append(plugin.Builtins(), plugin.BuiltinRegistration{
			Manifest: plugin.Manifest{ID: "scratch-probe", Provides: plugin.Provides{
				Tools: []plugin.ToolSpec{{Name: scratchTool, Description: "records its caller"}}}},
			Handlers: probeHandlers{rec}, Grants: []string{"read"},
		})
	}
}

func scratchCoreReg(rec *recorder) mcp.CoreRegistration {
	return mcp.CoreRegistration{Tool: mcp.Tool{Name: scratchCore, Description: "in-process core"}, Grants: []string{"read"},
		Handler: func(ctx context.Context, _ []byte) ([]byte, error) {
			rec.note(ctx, scratchCore)
			return []byte(`{}`), nil
		}}
}

// serveForTest serves h on a fresh owner socket until the test ends.
func serveForTest(t *testing.T, h *rpc.Handler, connCtx connContextFunc) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "s.sock")
	serveUntilCleanup(t, sock, func(ctx context.Context) error {
		ln, err := runtime.ListenOwnerSocket(sock)
		if err != nil {
			return err
		}
		defer func() { _ = ln.Close() }()
		return serveRPCListener(ctx, ln, h, connCtx)
	})
	return sock
}

// startFakeDaemon serves methods on a daemon-shaped registry whose resolver
// admits any token as a scoped principal and records it.
func startFakeDaemon(t *testing.T, methods ...string) (sock string, rec *recorder, tokens func() []string) {
	t.Helper()
	rec = &recorder{name: daemonName}
	var mu sync.Mutex
	var got []string
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, tok)
		return principal.Principal{ID: "fake-p", Kind: principal.KindSession, SessionID: "fake-s", Scope: "project:fake"}, nil
	})
	for _, m := range methods {
		reg.Register(m, rec.handler(m))
	}
	sock = serveForTest(t, rpc.NewHandler(reg), rpc.ConnContext)
	return sock, rec, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

// dialTo is the production token-binding dialer for sock.
func dialTo(sock string) func(string) (*client.Client, error) { return dialMCPDaemonAt(sock) }

func fixedEnv(token string) func(string) string {
	return func(k string) string {
		if k == principal.EnvToken {
			return token
		}
		return ""
	}
}

// mcpSocketPost sends one mcp.dispatch carrying frame, with exactly the
// given extra headers, and returns the status and body.
func mcpSocketPost(t *testing.T, sock string, header map[string]string, frame string) (int, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"mcp.dispatch","params":` + frame + `}`
	conn, err := client.UnixDialer(context.Background(), sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var b strings.Builder
	fmt.Fprintf(&b, "POST /rpc HTTP/1.1\r\nHost: unix\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: %d\r\n", len(body))
	for k, v := range header {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n" + body)
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatalf("write: %v", err)
	}
	return readMCPResponse(t, bufio.NewReader(conn))
}

// readMCPResponse reads the status line, the headers and the whole body,
// chunked or not (the connection closes after one response).
func readMCPResponse(t *testing.T, br *bufio.Reader) (int, string) {
	t.Helper()
	line, err := br.ReadString('\n')
	fields := strings.Fields(line)
	if err != nil || len(fields) < 2 {
		t.Fatalf("status line %q: %v", line, err)
	}
	chunked := false
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("response headers: %v", err)
		}
		if l = strings.TrimRight(l, "\r\n"); l == "" {
			break
		}
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Transfer-Encoding") {
			chunked = strings.Contains(strings.ToLower(v), "chunked")
		}
	}
	var body io.Reader = br
	if chunked {
		body = httputil.NewChunkedReader(br)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("response body: %v", err)
	}
	code, _ := strconv.Atoi(fields[1])
	return code, string(b)
}

// stdioExchange runs serveStdioReal over an io.Pipe until EOF.
func stdioExchange(t *testing.T, tools *mcp.ToolRegistry, frames ...string) string {
	t.Helper()
	inR, inW := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serveStdioReal(context.Background(), tools, inR, &out) }()
	for _, f := range frames {
		if _, err := io.WriteString(inW, f+"\n"); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveStdioReal: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveStdioReal did not return at EOF")
	}
	return out.String()
}

func toolNameFor(t *testing.T, method string) string {
	t.Helper()
	for _, s := range coretools.Specs() {
		if s.Method == method {
			return s.Name
		}
	}
	t.Fatalf("no coretools spec for %s", method)
	return ""
}

func listed(tools *mcp.ToolRegistry, name string) bool {
	for _, tool := range tools.List() {
		if tool.Name == name {
			return true
		}
	}
	return false
}
