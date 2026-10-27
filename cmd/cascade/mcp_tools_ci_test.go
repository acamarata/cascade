// Purpose: the cascade_ci_run and cascade_ci_status MCP tools through the
// real `cascade mcp serve` composition: listed only under a granting
// policy, and cascade_ci_run really runs the configured gate in the
// server's working directory against a real git repository.
// Inputs: a temp CASCADE_HOME, a temp git repository, and shell-builtin
// steps that behave the same under sh and cmd.
// Constraints: HOME and USERPROFILE are redirected into the temp tree; no
// timing and no path separators in assertions.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ciServeFixture is the serve registry plus the repository it runs in.
type ciServeFixture struct {
	tools *mcp.ToolRegistry
	repo  string
}

// newCIServeFixture builds the real serve registry over a temp CASCADE_HOME
// whose [ci.local] steps write marker.txt, with filter as the policy and
// getenv as the process environment. The working directory is a fresh git
// repository.
func newCIServeFixture(t *testing.T, filter mcp.CapabilityFilter, getenv func(string) string,
	dial func(string) (*client.Client, error)) ciServeFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; the gate needs a real repository")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	paths := doctorTestPaths(t)
	toml := "[ci.local]\nlint = [\"exit 0\"]\ntest = [\"echo ran>marker.txt\"]\nbuild = [\"exit 0\"]\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(toml), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(repo)

	wiring := buildMCPToolWiring(context.Background(), paths, runtime.SystemClock{})
	t.Cleanup(wiring.Close)
	wiring.Filter = filter
	return ciServeFixture{tools: mcpServeTools(wiring, getenv, dial, noBuiltinTools, nil), repo: repo}
}

func noEnv(string) string { return "" }

// TestMCPServeListsCITools: both tools are listed under a granting policy,
// carry no repository or command property, and are withheld when nothing
// grants them.
func TestMCPServeListsCITools(t *testing.T) {
	granted := newCIServeFixture(t, grantEverything{}, noEnv, nil)
	byName := map[string]mcp.Tool{}
	for _, tool := range granted.tools.List() {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"cascade_ci_run", "cascade_ci_status"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s is not listed under a granting policy", name)
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("%s reached the registry with no properties in its schema", name)
		}
		for _, banned := range []string{"repo", "path", "cwd", "command"} {
			if _, present := props[banned]; present {
				t.Errorf("%s exposes a %q property", name, banned)
			}
		}
		if tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s permits additional properties", name)
		}
	}

	denied := newCIServeFixture(t, mcp.DenyAllFilter{}, noEnv, nil)
	for _, tool := range denied.tools.List() {
		if tool.Name == "cascade_ci_run" || tool.Name == "cascade_ci_status" {
			t.Errorf("%s is listed by a registry that grants nothing", tool.Name)
		}
	}
	if len(denied.tools.FilteredOut()) == 0 {
		t.Error("a deny-all registry filtered nothing; the filter was never consulted")
	}
}

// TestMCPCIRunEndToEnd: cascade_ci_run executes the configured steps in
// the server's working directory and returns names and exit codes only; the
// run is then visible through cascade_ci_status; a repository argument is
// refused.
func TestMCPCIRunEndToEnd(t *testing.T) {
	fx := newCIServeFixture(t, grantEverything{}, noEnv, nil)
	ctx := context.Background()

	if _, err := fx.tools.Call(ctx, "cascade_ci_run", []byte(`{"repo":"/elsewhere"}`)); err == nil {
		t.Fatal("cascade_ci_run accepted a repo argument")
	}
	if _, err := os.Stat(filepath.Join(fx.repo, "marker.txt")); err == nil {
		t.Fatal("a refused call still ran a step")
	}

	out, err := fx.tools.Call(ctx, "cascade_ci_run", []byte(`{"test_only":true}`))
	if err != nil {
		t.Fatalf("cascade_ci_run: %v", err)
	}
	var run struct {
		RunID  int64 `json:"run_id"`
		Passed bool  `json:"passed"`
		Steps  []struct {
			Name     string `json:"name"`
			ExitCode int    `json:"exit_code"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("decoding %s: %v", out, err)
	}
	if !run.Passed || run.RunID == 0 || len(run.Steps) != 1 || run.Steps[0].Name != "test" || run.Steps[0].ExitCode != 0 {
		t.Fatalf("cascade_ci_run = %s, want one passing test step with a run id", out)
	}
	if _, err := os.Stat(filepath.Join(fx.repo, "marker.txt")); err != nil {
		t.Errorf("the step did not run in the server's working directory: %v", err)
	}

	status, err := fx.tools.Call(ctx, "cascade_ci_status", []byte(`{"limit":5}`))
	if err != nil {
		t.Fatalf("cascade_ci_status: %v", err)
	}
	if !strings.Contains(string(status), `"source":"local"`) {
		t.Errorf("cascade_ci_status = %s, want the local run just recorded", status)
	}
}

// TestMCPCIRunRefusedInPrincipalMode: a process carrying a caller principal
// token must not run local commands in-process. ci.run is owner-only, so
// applyPrincipalMode re-binds it to the daemon forwarder; with no daemon the
// call is refused unreachable and no step runs.
func TestMCPCIRunRefusedInPrincipalMode(t *testing.T) {
	getenv := func(k string) string {
		if k == principal.EnvToken {
			return "fake-principal-token"
		}
		return ""
	}
	dial := func(string) (*client.Client, error) { return nil, errors.New("no daemon in this test") }
	fx := newCIServeFixture(t, grantEverything{}, getenv, dial)

	_, err := fx.tools.Call(context.Background(), "cascade_ci_run", []byte(`{}`))
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("a principal-bearing process: err = %v, want the daemon forwarder's unavailable refusal", err)
	}
	if _, err := os.Stat(filepath.Join(fx.repo, "marker.txt")); err == nil {
		t.Error("a step ran for a principal-bearing process")
	}
}

// ciStdioExchange writes frames to the real stdio transport (serveStdioReal,
// the one `cascade mcp serve --stdio` runs) and returns everything it wrote
// back once the input closes.
func ciStdioExchange(t *testing.T, tools *mcp.ToolRegistry, frames ...string) string {
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
	case <-time.After(30 * time.Second):
		t.Fatal("serveStdioReal did not return at EOF")
	}
	return out.String()
}

// TestMCPCIToolsOverTheStdioWire: a client session initialises, lists the
// tools, then calls cascade_ci_status and cascade_ci_run against a real git
// repository over the real stdio transport.
func TestMCPCIToolsOverTheStdioWire(t *testing.T) {
	fx := newCIServeFixture(t, grantEverything{}, noEnv, nil)
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"qa","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"cascade_ci_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"cascade_ci_run","arguments":{"test_only":true}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"cascade_ci_run","arguments":{"repo":"/elsewhere"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"cascade_ci_status","arguments":{}}}`,
	}
	out := ciStdioExchange(t, fx.tools, frames...)
	t.Logf("transcript:\n%s", out)

	for _, want := range []string{`"name":"cascade_ci_run"`, `"name":"cascade_ci_status"`, `\"passed\":true`, `\"source\":\"local\"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the transcript lacks %s", want)
		}
	}
	if _, err := os.Stat(filepath.Join(fx.repo, "marker.txt")); err != nil {
		t.Errorf("the step did not run in the server's working directory: %v", err)
	}
	// The refused call (id 5) is a JSON-RPC error, not a result.
	if !strings.Contains(out, `"id":5,"error"`) {
		t.Errorf("the repo argument was not refused: %s", out)
	}
}

// TestCIMethodsAreOwnerOnly pins the declared access class itself, through
// the same admission check the principal-mode probe uses: an owner caller is
// admitted, a caller carrying a principal and a caller-less context are
// refused as permission denied. An undeclared method fails as internal and an
// open one admits the principal, so removing or loosening the declaration
// turns this red.
func TestCIMethodsAreOwnerOnly(t *testing.T) {
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	for _, m := range []string{"ci.run", "ci.status"} {
		reg.Register(m, func(context.Context, json.RawMessage) (any, error) { return map[string]bool{"ran": true}, nil })
	}
	cases := []struct {
		who  string
		ctx  context.Context
		want string
	}{
		{"owner", principal.WithCaller(context.Background(), principal.Caller{}), ""},
		{"principal", principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "fake-principal"}}), "permission-denied"},
		{"no caller", context.Background(), "permission-denied"},
	}
	for _, m := range []string{"ci.run", "ci.status"} {
		for _, c := range cases {
			_, eo := reg.Dispatch(c.ctx, &rpc.Request{JSONRPC: "2.0", Method: m, Params: json.RawMessage(`{}`)})
			got := ""
			if eo != nil {
				kind, _ := cascade.KindFromJSONRPCCode(eo.Code)
				got = kind.String()
			}
			if got != c.want {
				t.Errorf("%s as %s: kind %q, want %q", m, c.who, got, c.want)
			}
		}
	}
}
