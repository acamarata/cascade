//go:build !windows && integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/sys/unix"
)

type localReadProbe struct {
	daemon *rpc.Registry
	thread string
	rec    recorder
}

// handler exercises unexposed chat verbs using the caller stamped by MCP.
func (p *localReadProbe) handler(t *testing.T) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		var out []any
		for _, req := range p.requests() {
			p.rec.note(ctx, req.Method)
			res, eo := p.daemon.Dispatch(ctx, &req)
			if req.Method == "chat.get_thread" {
				if eo == nil || eo.Code != cascade.KindPermissionDenied.JSONRPCCode() || res != nil {
					t.Errorf("MCP get_thread = %v, %+v; want permission denied", res, eo)
				}
			} else if eo != nil {
				t.Errorf("MCP %s = %+v", req.Method, eo)
			}
			raw, err := json.Marshal(res)
			if err != nil || strings.Contains(string(raw), p.thread) || strings.Contains(string(raw), "privatebeacon") {
				t.Errorf("MCP %s leaked thread: %s (%v)", req.Method, raw, err)
			}
			out = append(out, map[string]any{"result": res, "error": eo})
		}
		return out, nil
	}
}

func (p *localReadProbe) requests() []rpc.Request {
	raw, _ := json.Marshal(map[string]string{"thread_id": p.thread})
	return []rpc.Request{
		{Method: "chat.list_threads", Params: json.RawMessage(`{}`)},
		{Method: "chat.get_thread", Params: raw},
		{Method: "chat.search", Params: json.RawMessage(`{"query":"privatebeacon"}`)},
	}
}

func (p *localReadProbe) assertStamped(t *testing.T) {
	t.Helper()
	seen := p.rec.all()
	if len(seen) != 3 {
		t.Fatalf("MCP drove %d chat verbs, want 3", len(seen))
	}
	for _, call := range seen {
		if !call.ok || call.caller.OwnerCLI || call.caller.Principal != nil {
			t.Fatalf("%s caller = %+v, present=%v", call.method, call.caller, call.ok)
		}
	}
}

// TestLocalOnlyNeverServedOverMCP uses real daemon and MCP compositions.
func TestLocalOnlyNeverServedOverMCP(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv(principal.EnvToken, "")
	c := composeForTest(t)
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	params := json.RawMessage(`{"role":"user","privacy_mode":"local-only","segments":[{"kind":"text","content":"privatebeacon"}]}`)
	res, eo := c.Registry.Dispatch(ctx, &rpc.Request{Method: "chat.append_turn", Params: params})
	if eo != nil {
		t.Fatal(eo)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ThreadID == "" {
		t.Fatal(err, string(raw))
	}
	p := &localReadProbe{daemon: c.Registry, thread: created.ThreadID}
	for _, req := range p.requests() {
		res, eo := c.Registry.Dispatch(ctx, &req)
		raw, err := json.Marshal(res)
		if eo != nil || err != nil || !strings.Contains(string(raw), created.ThreadID) {
			t.Fatalf("owner control %s = %s, %+v, %v", req.Method, raw, eo, err)
		}
	}
	for name, header := range map[string]map[string]string{
		"socket-no-header": nil, "socket-owner-marker": {principal.HeaderOwnerCLI: "owner-cli"},
	} {
		t.Run(name, func(t *testing.T) { testLocalOnlyMCPSocket(t, c, created.ThreadID, header) })
	}
	t.Run("stdio", func(t *testing.T) { testLocalOnlyMCPStdio(t, c, created.ThreadID) })
}

// localOnlyMCPTools mounts the daemon probe on the real stdio method registry.
// None of the three chat verbs currently has a core tool spec, so the
// context.slice entry point drives all three with its transport's caller.
func localOnlyMCPTools(t *testing.T, c composedForTest, thread string) (*mcp.ToolRegistry, *localReadProbe) {
	t.Helper()
	wiring := buildMCPToolWiring(context.Background(), doctorTestPaths(t), c.Opts.Clock)
	t.Cleanup(wiring.Close)
	probe := &localReadProbe{daemon: c.Registry, thread: thread}
	wiring.Methods.Register("context.slice", probe.handler(t))
	wiring.Filter = grantEverything{}
	return mcpServeTools(wiring, fixedEnv(""), nil, noBuiltinTools, nil), probe
}

func testLocalOnlyMCPSocket(t *testing.T, c composedForTest, thread string, header map[string]string) {
	t.Helper()
	tools, probe := localOnlyMCPTools(t, c, thread)
	reg, err := buildSocketMCPRegistry(tools, daemon.RefuseAllElevation(c.Opts.Clock))
	if err != nil {
		t.Fatal(err)
	}
	// Start at the registry boundary with the caller HTTP admission supplies.
	// The real socket registry must clear even an admitted owner CLI marker.
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: header[principal.HeaderOwnerCLI] == "owner-cli"})
	res, eo := reg.Dispatch(ctx, &rpc.Request{Method: transport.MCPMethod,
		Params: json.RawMessage(callFrame(toolNameFor(t, "context.slice")))})
	body, err := json.Marshal(res)
	if eo != nil || err != nil || strings.Contains(string(body), thread) || strings.Contains(string(body), "privatebeacon") {
		t.Fatalf("MCP socket registry = %s, %+v, %v", body, eo, err)
	}
	probe.assertStamped(t)
}

func testLocalOnlyMCPStdio(t *testing.T, c composedForTest, thread string) {
	t.Helper()
	tools, probe := localOnlyMCPTools(t, c, thread)
	out := stdioExchange(t, tools, callFrame(toolNameFor(t, "context.slice")))
	if strings.Contains(out, thread) || strings.Contains(out, "privatebeacon") {
		t.Fatalf("MCP stdio leaked thread: %s", out)
	}
	probe.assertStamped(t)
}

// TestOwnerCLIKeepsOwnerView checks real client headers against daemon admission.
func TestOwnerCLIKeepsOwnerView(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	c.Registry.Register("vault.get", func(context.Context, json.RawMessage) (any, error) {
		t.Error("principal reached vault backend")
		return nil, nil
	})
	cli, token := localOnlyOwnerClient(t, c)
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	params := map[string]any{"role": "user", "segments": []map[string]string{{"kind": "text", "content": "private fixture"}}, "privacy_mode": "local-only"}
	if err := cli.Do(context.Background(), "chat.append_turn", params, &created); err != nil || created.ThreadID == "" {
		t.Fatal(created, err)
	}
	var owner json.RawMessage
	if err := cli.Do(context.Background(), "chat.list_threads", nil, &owner); err != nil || !bytes.Contains(owner, []byte(created.ThreadID)) {
		t.Fatal(string(owner), err)
	}
	t.Setenv(principal.EnvToken, token)
	var scoped json.RawMessage
	err := cli.Do(context.Background(), "chat.list_threads", nil, &scoped)
	if err != nil || bytes.Contains(scoped, []byte(created.ThreadID)) {
		t.Fatal("owner chat thread leaked to a principal", string(scoped), err)
	}
	if err := cli.Do(context.Background(), "vault.get", nil, nil); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatal(err)
	}
}

// localOnlyOwnerClient retains RPC admission without binding a network listener.
func localOnlyOwnerClient(t *testing.T, c composedForTest) (*client.Client, string) {
	t.Helper()
	m := principal.NewMinter(c.Opts.Clock)
	token, err := m.Mint(principal.Principal{ID: "test", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Registry.SetAccessPolicy(func(token string) (principal.Principal, error) { return m.Resolve(token, c.Opts.Clock.Now()) })
	h := rpc.NewHandler(c.Registry)
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		peer, server := localOnlySocketPair(t)
		go func() {
			defer func() { _ = server.Close() }()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = req.Body.Close() }()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req.WithContext(rpc.ConnContext(ctx, server)))
			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()
			resp.Close = true
			if err := resp.Write(server); err != nil {
				t.Error(err)
			}
		}()
		return peer, nil
	}
	return client.New("local-pair", dial, 5*time.Second), token
}

func localOnlySocketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]net.Conn
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "local-pair")
		conns[i], err = net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return conns[0], conns[1]
}
