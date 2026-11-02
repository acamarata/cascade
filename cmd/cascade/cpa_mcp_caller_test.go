//go:build !windows && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
)

type cpaCallerRig struct {
	c      composedForTest
	cli    *client.Client
	socket string
	bridge chan http.Header
	minter *principal.Minter
}

func newCPACallerRig(t *testing.T) cpaCallerRig {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, root)
	}
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatal(err)
	}
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	deps.Paths = fakeDaemonPaths{root: root}
	deps.Clock = runtime.NewFixedClock(time.Unix(1800000000, 0))
	var c composedForTest
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, e *rpc.SSEMux) { c.Registry, c.Events = r, e })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	c.Opts = opts
	m := principal.NewMinter(opts.Clock)
	c.Registry.SetAccessPolicy(func(token string) (principal.Principal, error) { return m.Resolve(token, opts.Clock.Now()) })
	bridge := make(chan http.Header, 20)
	srv := cpaRecordingServer(t, c.Registry, bridge)
	// Keep the socket inside t.TempDir without putting the harness's long
	// absolute temp prefix into sockaddr_un or the private bind name.
	t.Chdir(root)
	socket := "d.sock"
	ln, err := runtime.ListenOwnerSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("CASCADE_SOCKET", socket)
	return cpaCallerRig{c, client.New(socket, client.UnixDialer, 5*time.Second), socket, bridge, m}
}

func cpaRecordingServer(t *testing.T, reg *rpc.Registry, bridge chan<- http.Header) *http.Server {
	t.Helper()
	h := rpc.NewHandler(reg)
	return &http.Server{ConnContext: rpc.ConnContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req rpc.Request
		if json.Unmarshal(body, &req) == nil && req.Method == "bridge.send" {
			bridge <- r.Header.Clone()
		}
		h.ServeHTTP(w, r)
	})}
}

func cpaSeedTurn(t *testing.T, cli *client.Client, tier, text string) string {
	t.Helper()
	var res struct {
		ThreadID string `json:"thread_id"`
	}
	params := map[string]any{"role": "user", "privacy_mode": tier, "segments": []map[string]string{{"kind": "text", "content": text}}}
	if err := cli.Do(context.Background(), "chat.append_turn", params, &res); err != nil || res.ThreadID == "" {
		t.Fatal(res, err)
	}
	return res.ThreadID
}

func cpaFrame(name, args string) string {
	return `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func TestCPAToolsNeverServeLocalOnly(t *testing.T) {
	rig := newCPACallerRig(t)
	local := cpaSeedTurn(t, rig.cli, "local-only", "privatebeacon")
	visible := cpaSeedTurn(t, rig.cli, "restricted", "sharedbeacon")
	cpaAssertOwnerReads(t, rig.cli, local)
	hosts := cpaToolHosts(t, rig)
	for name, call := range hosts {
		t.Run(name, func(t *testing.T) { cpaAssertPrivateReads(t, call, local, visible) })
	}
	// A send on an existing minted thread also asks bridge.send whether it is bound.
	out := hosts["stdio"](cpaFrame("cascade_cpa_send", `{"content":"next","sensitivity":"restricted","thread_id":"`+visible+`"}`))
	if strings.Contains(out, `"error"`) {
		t.Fatal(out)
	}
	select {
	case h := <-rig.bridge:
		if h.Get(principal.HeaderOwnerCLI) != "owner-model" || len(h.Values(principal.HeaderToken)) != 0 {
			t.Fatal("bridge.send did not use owner-model")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge.send was not called")
	}
}

func cpaAssertOwnerReads(t *testing.T, cli *client.Client, local string) {
	t.Helper()
	for _, method := range []string{"chat.list_threads", "chat.get_thread", "chat.search"} {
		params := map[string]string{"thread_id": local}
		if method == "chat.list_threads" {
			params = nil
		}
		if method == "chat.search" {
			params = map[string]string{"query": "privatebeacon"}
		}
		var raw json.RawMessage
		if err := cli.Do(context.Background(), method, params, &raw); err != nil || !strings.Contains(string(raw), local) {
			t.Fatal("CLI positive control", method, string(raw), err)
		}
	}
}

func cpaToolHosts(t *testing.T, rig cpaCallerRig) map[string]func(string) string {
	t.Helper()
	tools := mcp.NewToolRegistry(plugin.Builtins, grantEverything{})
	reg, err := buildSocketMCPRegistry(tools, daemon.RefuseAllElevation(rig.c.Opts.Clock))
	if err != nil {
		t.Fatal(err)
	}
	socket := serveForTest(t, rpc.NewHandler(reg), rpc.ConnContext)
	stdio := productionMCPDeps().NewTools()
	return map[string]func(string) string{
		"socket": func(frame string) string {
			_, out := mcpSocketPost(t, socket, map[string]string{principal.HeaderOwnerCLI: "owner-cli"}, frame)
			return out
		},
		"stdio": func(frame string) string { return stdioExchange(t, stdio, frame) },
		"daemon": func(frame string) string {
			var out json.RawMessage
			if err := rig.cli.Do(context.Background(), transport.MCPMethod, json.RawMessage(frame), &out); err != nil {
				t.Fatal(err)
			}
			return string(out)
		},
	}
}

func cpaAssertPrivateReads(t *testing.T, call func(string) string, local, visible string) {
	t.Helper()
	listing := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, tool := range []string{"cascade_cpa_history", "cascade_cpa_search", "cascade_cpa_send"} {
		if !strings.Contains(listing, tool) {
			t.Fatalf("tools/list missing %s: %s", tool, listing)
		}
	}
	out := call(cpaFrame("cascade_cpa_history", `{}`))
	if !strings.Contains(out, visible) || strings.Contains(out, local) {
		t.Fatal("history list", out)
	}
	out = call(cpaFrame("cascade_cpa_history", `{"thread_id":"`+local+`"}`))
	if !strings.Contains(out, "permission-denied") || strings.Contains(out, "privatebeacon") {
		t.Fatal("local read", out)
	}
	out = call(cpaFrame("cascade_cpa_search", `{"query":"sharedbeacon"}`))
	if !strings.Contains(out, visible) || !strings.Contains(out, "sharedbeacon") {
		t.Fatal("search positive control", out)
	}
	out = call(cpaFrame("cascade_cpa_search", `{"query":"privatebeacon"}`))
	if strings.Contains(out, local) || strings.Contains(out, "privatebeacon") || strings.Contains(out, `"error"`) {
		t.Fatal("private search", out)
	}
}

func TestCPAToolsForwardPrincipalToken(t *testing.T) {
	rig := newCPACallerRig(t)
	// Construct the owner tool registry before supplying a process credential:
	// this exercises the adapter's per-call token forwarding rule.
	tools := productionMCPDeps().NewTools()
	token := cpaMintToken(t, rig, "own", "project:own")
	t.Setenv(principal.EnvToken, token)
	own := cpaSeedTurn(t, rig.cli, "restricted", "ownbeacon")
	otherToken := cpaMintToken(t, rig, "other", "project:other")
	t.Setenv(principal.EnvToken, otherToken)
	other := cpaSeedTurn(t, rig.cli, "restricted", "otherbeacon")
	t.Setenv(principal.EnvToken, token)
	out := stdioExchange(t, tools, cpaFrame("cascade_cpa_history", `{}`))
	if !strings.Contains(out, own) || strings.Contains(out, other) {
		t.Fatal("principal scope", out)
	}
	out = stdioExchange(t, tools, cpaFrame("cascade_cpa_history", `{"thread_id":"`+other+`"}`))
	if !strings.Contains(out, "permission-denied") || strings.Contains(out, "otherbeacon") {
		t.Fatal(out)
	}
	// Exclusive token headers are also required for successful admission:
	// the daemon rejects any token plus marker pair before scoping.
}

func cpaMintToken(t *testing.T, rig cpaCallerRig, id, scope string) string {
	t.Helper()
	token, err := rig.minter.Mint(principal.Principal{ID: id, Kind: principal.KindSession, Scope: scope}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
