//go:build !windows

// Purpose: a token-bearing stdio MCP process forwards every gated read to
//   the daemon with its token and refuses when the daemon is unreachable; a
//   tokenless one keeps the owner view; the daemon's own mcp.dispatch
//   re-checks each core tool's inner method access. Fixtures live in
//   mcp_principal_fixtures_test.go.
// Inputs: injected getenv and dialers; fake daemons are real rpc.Handlers
//   on real unix sockets under a short temp dir (sun_path), dialed with the
//   first-party client; a test Minter on a frozen clock. Fixture tokens and
//   principals are obviously fake.
// Outputs: test failures naming the case.
// Constraints: imports neither "net" nor "net/http" (the no-network
//   unit-test gate).
// SPORT: cmd/cascade/mcp principal mode tests [ADD].

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	fakeToken     = "cpt1_FAKE-test-token-not-a-real-credential"
	inProcessName = "in-process-owner-view"
	daemonName    = "fake-daemon-scoped"
	scratchTool   = "scratch_caller_probe"
	scratchCore   = "scratch_core_probe"
	listFrame     = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
)

func TestStdioWithTokenServesNoInProcessScopedRead(t *testing.T) {
	t.Setenv(principal.EnvToken, fakeToken)
	methods := []string{"memory.recall", "chat.list_threads", "fleet.sessions.list"}
	sock, remote, tokens := startFakeDaemon(t, methods...)
	local, inproc := rpc.NewRegistry(), &recorder{name: inProcessName}
	for _, m := range methods {
		local.Register(m, inproc.handler(m))
	}
	tools := mcpServeTools(mcpToolWiring{Methods: local, Filter: grantEverything{}, Close: func() {}},
		os.Getenv, dialTo(sock), scratchSource(inproc), []mcp.CoreRegistration{scratchCoreReg(inproc)})

	out := stdioExchange(t, tools, callFrame(toolNameFor(t, "memory.recall")))
	if !strings.Contains(out, daemonName) || strings.Contains(out, inProcessName) {
		t.Fatalf("memory recall over stdio answered %q, want the daemon's scoped answer", out)
	}
	for _, m := range methods[1:] {
		res, eo := local.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: m, Params: json.RawMessage(`{}`)})
		raw, _ := res.(json.RawMessage)
		if eo != nil || !strings.Contains(string(raw), daemonName) {
			t.Fatalf("%s = %v, %+v; want the daemon's answer", m, res, eo)
		}
	}
	for _, m := range methods {
		if remote.count(m) != 1 {
			t.Errorf("daemon saw %s %d times, want 1", m, remote.count(m))
		}
	}
	got := tokens()
	if len(got) != len(methods) {
		t.Fatalf("daemon resolved %d tokens, want %d", len(got), len(methods))
	}
	for _, tok := range got {
		if tok != fakeToken {
			t.Fatalf("daemon got token %q, want the process token", tok)
		}
	}
	if n := inproc.count(""); n != 0 {
		t.Fatalf("in-process handlers ran %d times in token mode, want 0", n)
	}
	if listed(tools, scratchTool) || listed(tools, scratchCore) {
		t.Fatal("token mode lists an in-process tool that bypasses the method table")
	}
	assertBoundTokenSurvivesEnvChange(t, local, remote, tokens)
}

// assertBoundTokenSurvivesEnvChange unsets, then changes, the token after the
// mode was chosen: each later call still carries the bound token and reaches
// the daemon as that principal, never as the owner CLI.
func assertBoundTokenSurvivesEnvChange(t *testing.T, local *rpc.Registry, remote *recorder, tokens func() []string) {
	t.Helper()
	for _, env := range []string{"", "cpt1_FAKE-other-token-not-real"} {
		t.Setenv(principal.EnvToken, env)
		if _, eo := local.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: "chat.list_threads"}); eo != nil {
			t.Fatalf("env %q: chat.list_threads refused: %+v", env, eo)
		}
	}
	if remote.count("chat.list_threads") != 3 {
		t.Fatalf("daemon saw chat.list_threads %d times, want 3", remote.count("chat.list_threads"))
	}
	for _, s := range remote.all() {
		if !s.ok || s.caller.Principal == nil || s.caller.OwnerCLI {
			t.Fatalf("daemon saw %s as %+v ok=%v, want the bound principal", s.method, s.caller, s.ok)
		}
	}
	for _, tok := range tokens() {
		if tok != fakeToken {
			t.Fatalf("daemon got token %q after an env change, want the bound token", tok)
		}
	}
}

// TestStdioWithoutGetenvServesNothing: with no getenv the mode is unknown,
// so nothing is listed or served in-process.
func TestStdioWithoutGetenvServesNothing(t *testing.T) {
	local, inproc := rpc.NewRegistry(), &recorder{name: inProcessName}
	local.Register("memory.recall", inproc.handler("memory.recall"))
	none := mcpServeTools(mcpToolWiring{Methods: local, Filter: grantEverything{}, Close: func() {}},
		nil, dialTo(filepath.Join(shortTempDir(t), "absent.sock")), scratchSource(inproc), []mcp.CoreRegistration{scratchCoreReg(inproc)})
	if got := none.List(); len(got) != 0 {
		t.Fatalf("nil getenv lists %v, want nothing", got)
	}
	if _, err := none.Call(context.Background(), toolNameFor(t, "memory.recall"), []byte(`{}`)); err == nil || inproc.count("") != 0 {
		t.Fatalf("nil getenv call = %v, in-process %d; want refused, 0", err, inproc.count(""))
	}
}

func TestStdioWithTokenDaemonDownRefuses(t *testing.T) {
	t.Setenv(principal.EnvToken, fakeToken)
	if _, err := dialMCPDaemon(nil)(fakeToken); err == nil {
		t.Fatal("dialMCPDaemon with no paths returned a client")
	}
	dials := map[string]func(string) (*client.Client, error){
		"dial-fails": func(string) (*client.Client, error) {
			return nil, cascade.New(cascade.KindUnavailable, "fake dial failure")
		},
		"daemon-stopped": dialTo(filepath.Join(shortTempDir(t), "absent.sock")),
		// A present but empty token is token mode too: refused, never dialed.
		"empty-token": func(string) (*client.Client, error) { return nil, errors.New("empty token must not dial") },
	}
	for name, dial := range dials {
		t.Run(name, func(t *testing.T) {
			getenv := fixedEnv(fakeToken)
			if name == "empty-token" {
				getenv = mcpPrincipalGetenv(func(k string) (string, bool) { return "", k == principal.EnvToken })
			}
			local, inproc := rpc.NewRegistry(), &recorder{name: inProcessName}
			for _, s := range coretools.Specs() {
				local.Register(s.Method, inproc.handler(s.Method))
			}
			tools := mcpServeTools(mcpToolWiring{Methods: local, Filter: grantEverything{}, Close: func() {}},
				getenv, dial, scratchSource(inproc), nil)
			names := tools.List()
			if len(names) != len(coretools.Registrations(local)) || len(names) == 0 {
				t.Fatalf("token mode lists %d tools, want every core tool", len(names))
			}
			for _, tool := range names {
				_, err := tools.Call(context.Background(), tool.Name, []byte(`{}`))
				wantKind, wantText := cascade.KindUnavailable, "unreachable"
				if name == "empty-token" {
					wantKind, wantText = cascade.KindPermissionDenied, "token is empty"
				}
				if kind, _ := cascade.KindOf(err); kind != wantKind || !strings.Contains(err.Error(), wantText) {
					t.Errorf("%s: err = %v, want %v %q", tool.Name, err, wantKind, wantText)
				}
			}
			if n := inproc.count(""); n != 0 {
				t.Fatalf("in-process handlers ran %d times with the daemon down, want 0", n)
			}
		})
	}
}

func TestStdioWithoutTokenKeepsOwnerView(t *testing.T) {
	local, inproc := rpc.NewRegistry(), &recorder{name: inProcessName}
	for _, s := range coretools.Specs() {
		local.Register(s.Method, inproc.handler(s.Method))
	}
	dialed := 0
	dial := func(string) (*client.Client, error) { dialed++; return nil, errors.New("must not dial") }
	core := []mcp.CoreRegistration{scratchCoreReg(inproc)}
	tools := mcpServeTools(mcpToolWiring{Methods: local, Filter: grantEverything{}, Close: func() {}},
		fixedEnv(""), dial, scratchSource(inproc), core)
	head := mcp.NewToolRegistry(scratchSource(inproc), grantEverything{}, append(core, coretools.Registrations(local)...)...)
	if fmt.Sprint(tools.List()) != fmt.Sprint(head.List()) || !listed(tools, scratchTool) || !listed(tools, scratchCore) {
		t.Fatalf("tokenless tools = %v, want the HEAD owner view %v", tools.List(), head.List())
	}
	for _, s := range coretools.Specs() {
		out, err := tools.Call(context.Background(), s.Name, []byte(`{}`))
		if err != nil || !strings.Contains(string(out), inProcessName) {
			t.Fatalf("%s = %s, %v; want the in-process owner answer", s.Name, out, err)
		}
	}
	if dialed != 0 {
		t.Fatalf("tokenless process dialed the daemon %d times", dialed)
	}
}

func TestMCPDispatchInnerMethodAccessChecked(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	minter := principal.NewMinter(clock)
	d := rpc.NewRegistry()
	d.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, clock.Now()) })
	rec := &recorder{name: inProcessName}
	d.Register("context.slice", rec.handler("context.slice"))
	tools := daemonMCPToolRegistry(d, grantEverything{})
	if err := transport.RegisterSocketMCP(d, mcp.NewServer(tools)); err != nil {
		t.Fatalf("RegisterSocketMCP: %v", err)
	}
	sock := serveForTest(t, rpc.NewHandler(d), rpc.ConnContext)
	tok, err := minter.Mint(principal.Principal{ID: "fake-p-1", Kind: principal.KindSession, SessionID: "fake-s-1",
		Scope: "project:fake"}, time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	slice, hdr := toolNameFor(t, "context.slice"), map[string]string{principal.HeaderToken: tok}
	if code, body := mcpSocketPost(t, sock, hdr, listFrame); code != 200 || !strings.Contains(body, slice) {
		t.Fatalf("principal tools/list = %d %s, want it admitted", code, body)
	}
	if code, body := mcpSocketPost(t, sock, hdr, callFrame(slice)); code != 200 || !strings.Contains(body, "caller not permitted") {
		t.Fatalf("principal tools/call = %d %s, want the inner refusal", code, body)
	}
	p, err := minter.Resolve(tok, clock.Now())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, callErr := tools.Call(principal.WithCaller(context.Background(), principal.Caller{Principal: &p}), slice, []byte(`{}`))
	var ce *cascade.Error
	if !errors.As(callErr, &ce) || ce.Kind != cascade.KindPermissionDenied || ce.Msg != "mcp: context.slice: caller not permitted" {
		t.Fatalf("principal call err = %v, want KindPermissionDenied caller not permitted", callErr)
	}
	if rec.count("") != 0 {
		t.Fatalf("context.slice ran %d times for a principal", rec.count(""))
	}
	if code, body := mcpSocketPost(t, sock, ownerHeader, callFrame(slice)); code != 200 || rec.count("context.slice") != 1 {
		t.Fatalf("owner tools/call = %d %s, count %d; want served once", code, body, rec.count("context.slice"))
	}
}
