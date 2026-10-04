//go:build !windows

// Purpose: composeDaemon's surface record. composeDaemon is the one
//
//	construction of the store, background subsystems, options and wiring, so
//	the tests build it over a temp CASCADE_HOME and record what it produced:
//	RPC methods, manifest subsystems, SSE topics, RunOptions fields and the
//	MCP tool names the daemon's filter exposes.
//
// Constraints: the Upgrade fields only exist for a non-dev build hash, so the
//
//	dedicated TestDaemonWiringSurface run passes the buildHash ldflag; a
//	whole-package run records a dev hash and expects Upgrade to be nil.
//
// SPORT: cmd/cascade/daemon (composition surface tests, P1-CORE-01).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp/transport"
	"github.com/acamarata/cascade/internal/rpc"
)

// surfaceRecordEnv names the file TestDaemonWiringSurface writes its record
// to. Unset, the test still checks its invariants and records nothing.
const surfaceRecordEnv = "CASCADE_SURFACE_RECORD"

// composedForTest is what composeDaemon built, read through its observer.
type composedForTest struct {
	Opts     daemon.RunOptions
	Registry *rpc.Registry
	Events   *rpc.SSEMux
}

// composeForTest runs composeDaemon over a fresh HOME and CASCADE_HOME and
// runs every cleanup it registered when the test ends.
func composeForTest(t *testing.T) composedForTest {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	var got composedForTest
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, m *rpc.SSEMux) {
		got.Registry, got.Events = r, m
	})
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	if got.Registry == nil || got.Events == nil {
		t.Fatal("composeDaemon never handed its registry and events mux to the observer")
	}
	got.Opts = opts
	return got
}

// surfaceLines renders the wiring surface as sorted, prefixed lines.
func surfaceLines(t *testing.T, c composedForTest) []string {
	t.Helper()
	var lines []string
	for _, m := range c.Registry.Methods() {
		lines = append(lines, "method "+m)
	}
	for _, s := range c.Opts.Manifest.Snapshot() {
		lines = append(lines, fmt.Sprintf("subsystem %s state=%d", s.Name, s.State))
	}
	lines = append(lines, fmt.Sprintf("sse-default %t", c.Events.Default != nil))
	for topic := range c.Events.Topics {
		lines = append(lines, "sse-topic "+topic)
	}
	rv := reflect.ValueOf(c.Opts)
	for i := 0; i < rv.NumField(); i++ {
		if !rv.Field(i).IsZero() {
			lines = append(lines, "runoption "+rv.Type().Field(i).Name)
		}
	}
	tools := mcpToolNames(t, c.Registry)
	if len(tools) == 0 {
		t.Fatal("the daemon's MCP tools/list exposed no tool; the record would hide a dropped tool registry")
	}
	for _, name := range tools {
		lines = append(lines, "mcp-tool "+name)
	}
	sort.Strings(lines)
	return lines
}

// mcpToolNames dispatches an MCP tools/list through the registry's own
// mcp.dispatch method and returns the tool names the daemon exposes.
func mcpToolNames(t *testing.T, r *rpc.Registry) []string {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	res, eo := r.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: transport.MCPMethod, Params: frame})
	if eo != nil {
		t.Fatalf("mcp tools/list: %v", eo)
	}
	raw, ok := res.(json.RawMessage)
	if !ok {
		t.Fatalf("mcp tools/list returned %T, want json.RawMessage", res)
	}
	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	var names []string
	for _, tool := range env.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestDaemonWiringSurface records the composed surface and checks the
// invariants a silent drop would break: status.get, the registry count the
// manifest reports, the sessions topic, and Upgrade tracking the build hash.
func TestDaemonWiringSurface(t *testing.T) {
	c := composeForTest(t)
	lines := surfaceLines(t, c)
	text := strings.Join(lines, "\n") + "\n"
	for _, want := range []string{"method status.get\n", "method mcp.dispatch\n", "sse-topic fleet.sessions\n", "subsystem rpc-registry state=1\n", "subsystem jobs.scheduler state=1\n", "subsystem conductor.router state=1\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("surface record lacks %q", strings.TrimSpace(want))
		}
	}
	wantDetail := fmt.Sprintf("%d methods", len(c.Registry.Methods()))
	found := false
	for _, s := range c.Opts.Manifest.Snapshot() {
		if s.Name == "rpc-registry" {
			found = true
			if s.Detail != wantDetail {
				t.Errorf("rpc-registry detail = %q, want %q", s.Detail, wantDetail)
			}
		}
	}
	if !found {
		t.Error("manifest has no rpc-registry entry")
	}
	if devHash := daemon.BuildHash() == "dev"; devHash == (c.Opts.Upgrade != nil) {
		t.Errorf("Upgrade set = %t with build hash %q; want it set exactly for a non-dev hash",
			c.Opts.Upgrade != nil, daemon.BuildHash())
	}
	if path := os.Getenv(surfaceRecordEnv); path != "" {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatalf("write surface record: %v", err)
		}
	}
}
