//go:build !windows

// Purpose: the MCP socket and stdio caller rules: a token peer is refused
//   on every non-Open verb, a header-less or owner-marker peer is the owner
//   (never the owner CLI), no owner-admin verb is registered, and every
//   core tool method carries an access class. Fixtures live in
//   mcp_principal_fixtures_test.go.
// Inputs: real unix sockets under a short temp dir; fake tokens.
// Outputs: test failures naming the case.
// Constraints: imports neither "net" nor "net/http".
// SPORT: cmd/cascade/mcp principal mode tests [ADD].

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tokenRefusal = "mcp socket: caller principals are not admitted here"

var (
	tokenHeader = map[string]string{principal.HeaderToken: fakeToken}
	ownerHeader = map[string]string{principal.HeaderOwnerCLI: "owner-cli"}
)

// startSocketMCP serves buildSocketMCPRegistry over core tools whose
// context.slice and memory.recall answer in-process (behind the access
// policy, so a caller-less dispatch refuses) plus the scratch plugin tool.
func startSocketMCP(t *testing.T) (sock string, reg *rpc.Registry, rec *recorder) {
	t.Helper()
	rec = &recorder{name: inProcessName}
	inner := rpc.NewRegistry()
	inner.SetAccessPolicy(nil)
	for _, m := range []string{"context.slice", "memory.recall"} {
		inner.Register(m, rec.handler(m))
	}
	tools := mcp.NewToolRegistry(scratchSource(rec), grantEverything{}, coretools.Registrations(inner)...)
	reg, err := buildSocketMCPRegistry(tools, daemon.RefuseAllElevation(runtime.SystemClock{}))
	if err != nil {
		t.Fatalf("buildSocketMCPRegistry: %v", err)
	}
	sock = serveForTest(t, rpc.NewHandler(reg), mcpSocketConnContext)
	return sock, reg, rec
}

func TestMCPSocketPrincipalTokenRefused(t *testing.T) {
	sock, reg, rec := startSocketMCP(t)
	slice := toolNameFor(t, "context.slice")
	if len(reg.Methods()) == 0 {
		t.Fatal("socket registry serves no method")
	}
	for _, m := range reg.Methods() {
		if m != transport.MCPMethod {
			t.Fatalf("socket registry serves %s; extend this test to cover it", m)
		}
		for _, frame := range []string{listFrame, callFrame(slice), callFrame(scratchTool)} {
			code, body := mcpSocketPost(t, sock, tokenHeader, frame)
			if code != 403 || !strings.Contains(body, tokenRefusal) {
				t.Fatalf("token peer %s %s = %d %s, want 403 %q", m, frame, code, body, tokenRefusal)
			}
		}
	}
	both := map[string]string{principal.HeaderToken: fakeToken, principal.HeaderOwnerCLI: "owner-cli"}
	if code, body := mcpSocketPost(t, sock, both, callFrame(slice)); code != 400 {
		t.Fatalf("both headers = %d %s, want 400", code, body)
	}
	// mcp.dispatch must not be Open: a caller-less request that skipped
	// admission is refused by its declared class.
	_, eo := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: transport.MCPMethod,
		Params: json.RawMessage(callFrame(slice))})
	if eo == nil || eo.Message != "caller not permitted" {
		t.Fatalf("caller-less mcp.dispatch = %+v, want the access refusal", eo)
	}
	if n := rec.count(""); n != 0 {
		t.Fatalf("a handler ran %d times for a refused peer", n)
	}
	if code, body := mcpSocketPost(t, sock, ownerHeader, callFrame(slice)); code != 200 || rec.count("context.slice") != 1 ||
		!strings.Contains(body, inProcessName) {
		t.Fatalf("owner marker = %d %s, want the tool served once", code, body)
	}
}

func TestMCPSocketNoHeaderIsOwner(t *testing.T) {
	sock, _, rec := startSocketMCP(t)
	slice := toolNameFor(t, "context.slice")
	if code, body := mcpSocketPost(t, sock, nil, listFrame); code != 200 || !strings.Contains(body, slice) {
		t.Fatalf("header-less tools/list = %d %s, want it served", code, body)
	}
	if code, body := mcpSocketPost(t, sock, nil, callFrame(slice)); code != 200 || !strings.Contains(body, inProcessName) {
		t.Fatalf("header-less tools/call = %d %s, want it served", code, body)
	}
	if rec.count("context.slice") != 1 {
		t.Fatalf("context.slice ran %d times, want 1", rec.count("context.slice"))
	}
	if code, body := mcpSocketPost(t, sock, tokenHeader, callFrame(slice)); code != 403 || !strings.Contains(body, tokenRefusal) {
		t.Fatalf("token peer = %d %s, want 403", code, body)
	}
	if rec.count("") != 1 {
		t.Fatalf("handlers ran %d times, want 1", rec.count(""))
	}
}

// assertOwnerCallers wants every recorded Caller present, principal-less
// and not the owner CLI, for both the core tool and the scratch plugin.
func assertOwnerCallers(t *testing.T, label string, rec *recorder) {
	t.Helper()
	got := map[string]int{}
	for _, s := range rec.all() {
		if !s.ok || s.caller.Principal != nil || s.caller.OwnerCLI {
			t.Fatalf("%s: %s saw caller %+v ok=%v, want the owner (nil principal, not the CLI)", label, s.method, s.caller, s.ok)
		}
		got[s.method]++
	}
	if got["context.slice"] == 0 || got[scratchTool] == 0 {
		t.Fatalf("%s: recorded %v, want context.slice and %s", label, got, scratchTool)
	}
}

func TestMCPTransportsNeverSetOwnerCLI(t *testing.T) {
	slice := toolNameFor(t, "context.slice")
	for name, header := range map[string]map[string]string{"socket-no-header": nil, "socket-owner-marker": ownerHeader} {
		sock, _, rec := startSocketMCP(t)
		for _, frame := range []string{callFrame(slice), callFrame(scratchTool)} {
			if code, body := mcpSocketPost(t, sock, header, frame); code != 200 || strings.Contains(body, `"error"`) {
				t.Fatalf("%s %s = %d %s, want served", name, frame, code, body)
			}
		}
		assertOwnerCallers(t, name, rec)
	}
	rec := &recorder{name: inProcessName}
	inner := rpc.NewRegistry()
	inner.SetAccessPolicy(nil)
	inner.Register("context.slice", rec.handler("context.slice"))
	tools := mcp.NewToolRegistry(scratchSource(rec), grantEverything{}, coretools.Registrations(inner)...)
	out := stdioExchange(t, tools, callFrame(slice), callFrame(scratchTool))
	if strings.Contains(out, `"error"`) {
		t.Fatalf("stdio answered an error: %s", out)
	}
	assertOwnerCallers(t, "stdio", rec)
}

func TestMCPRegistriesRegisterNoOwnerAdminVerb(t *testing.T) {
	wiring := buildMCPToolWiring(context.Background(), doctorTestPaths(t), runtime.SystemClock{})
	t.Cleanup(wiring.Close)
	sockReg, err := buildSocketMCPRegistry(mcp.NewToolRegistry(scratchSource(&recorder{}), grantEverything{}),
		daemon.RefuseAllElevation(runtime.SystemClock{}))
	if err != nil {
		t.Fatalf("buildSocketMCPRegistry: %v", err)
	}
	for label, reg := range map[string]*rpc.Registry{"stdio wiring": wiring.Methods, "mcp socket": sockReg} {
		methods := reg.Methods()
		if len(methods) == 0 {
			t.Fatalf("%s registers no method; the check would be vacuous", label)
		}
		for _, m := range methods {
			for _, prefix := range []string{"vault.", "config.", "policy.", "approval.", "elevation.", "node."} {
				if strings.HasPrefix(m, prefix) {
					t.Errorf("%s registers owner-admin verb %s", label, m)
				}
			}
		}
	}
}

func TestEveryScopedToolClassified(t *testing.T) {
	for _, s := range coretools.Specs() {
		if !mcpAccessClassed(s.Method) {
			t.Errorf("coretools method %s has no caller-principal access class", s.Method)
		}
	}
	const unclassed = "scratch.unclassed.verb"
	if mcpAccessClassed(unclassed) {
		t.Fatal("a scratch undeclared method reads as classed")
	}
	t.Setenv(principal.EnvToken, fakeToken)
	sock, remote, _ := startFakeDaemon(t, unclassed, "chat.list_threads")
	local, inproc := rpc.NewRegistry(), &recorder{name: inProcessName}
	local.Register(unclassed, inproc.handler(unclassed))
	local.Register("chat.list_threads", inproc.handler("chat.list_threads"))
	if err := applyPrincipalMode(local, fixedEnv(fakeToken), dialTo(sock)); err != nil {
		t.Fatalf("applyPrincipalMode: %v", err)
	}
	_, eo := local.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: unclassed})
	want := "mcp: " + unclassed + " has no caller-principal access class; refused for a principal-bearing process"
	if eo == nil || !strings.HasSuffix(eo.Message, ": "+want) {
		t.Fatalf("unclassed method = %+v, want refusal %q", eo, want)
	}
	if kind, ok := cascade.KindFromJSONRPCCode(eo.Code); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("unclassed method = %+v, want refusal %q", eo, want)
	}
	if _, eo := local.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: "chat.list_threads"}); eo != nil {
		t.Fatalf("classed method refused: %+v", eo)
	}
	if inproc.count("") != 0 || remote.count(unclassed) != 0 || remote.count("chat.list_threads") != 1 {
		t.Fatalf("in-process %d, daemon unclassed %d, daemon chat %d; want 0, 0, 1",
			inproc.count(""), remote.count(unclassed), remote.count("chat.list_threads"))
	}
}

// TestDaemonMCPNonCoreToolsOwnerOnly drives the daemon's own mcp.dispatch:
// a builtin plugin tool and a backup core tool skip the method registry, so
// a principal admitted to the dispatcher is refused; the owner still reaches
// both, and a caller-less call refuses.
func TestDaemonMCPNonCoreToolsOwnerOnly(t *testing.T) {
	t.Setenv("HOME", shortTempDir(t))
	t.Setenv("CASCADE_HOME", shortTempDir(t))
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	minter := principal.NewMinter(clock)
	d := rpc.NewRegistry()
	d.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, clock.Now()) })
	tools := daemonMCPToolRegistry(d, grantEverything{})
	if err := transport.RegisterSocketMCP(d, mcp.NewServer(tools)); err != nil {
		t.Fatalf("RegisterSocketMCP: %v", err)
	}
	sock := serveForTest(t, rpc.NewHandler(d), rpc.ConnContext)
	tok, err := minter.Mint(principal.Principal{ID: "fake-p-2", Kind: principal.KindSession, SessionID: "fake-s-2",
		Scope: "project:fake"}, time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	p, err := minter.Resolve(tok, clock.Now())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, name := range []string{"greet-tool", "cascade_backup_list"} {
		if !listed(tools, name) {
			t.Fatalf("%s is not served by the daemon registry; the case would be vacuous", name)
		}
		code, body := mcpSocketPost(t, sock, map[string]string{principal.HeaderToken: tok}, callFrame(name))
		if code != 200 || !strings.Contains(body, "mcp: owner-only tool") {
			t.Fatalf("principal %s = %d %s, want the owner-only refusal", name, code, body)
		}
		for label, ctx := range map[string]context.Context{
			"principal": principal.WithCaller(context.Background(), principal.Caller{Principal: &p}),
			"no-caller": context.Background(),
		} {
			_, callErr := tools.Call(ctx, name, []byte(`{}`))
			var ce *cascade.Error
			if !errors.As(callErr, &ce) || ce.Kind != cascade.KindPermissionDenied || ce.Msg != "mcp: owner-only tool" {
				t.Fatalf("%s %s err = %v, want KindPermissionDenied owner-only tool", label, name, callErr)
			}
		}
		if code, body := mcpSocketPost(t, sock, ownerHeader, callFrame(name)); code != 200 || strings.Contains(body, "owner-only") {
			t.Fatalf("owner %s = %d %s, want it reached", name, code, body)
		}
	}
}
