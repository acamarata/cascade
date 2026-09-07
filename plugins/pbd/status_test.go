// Package pbd (status_test.go): status.go's tests. Real-client proofs
// (Art.2) use internal/rpc and internal/mcp directly, never self-authored.
// SPORT: plugins/pbd status/board (ADD) — P1-E14-W3-S29-T4.
package pbd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
	"github.com/acamarata/cascade/providers/sqlite"
)

// statusFixedClock is a constant-instant Clock double (never the wall
// clock) for pews.NewProjector.
type statusFixedClock struct{}

func (statusFixedClock) Now() time.Time { return time.Unix(0, 0) }

// mkStatusFixture writes a two-ticket tree: S/build and M/heavy.
func mkStatusFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-29", "tickets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	y1 := cleanLintTicketYAML("P1-E14-W3-S29-T1")
	y2 := cleanLintTicketYAML("P1-E14-W3-S29-T2")
	y2 = replaceInYAML(y2, "weight: S\n", "weight: M\n")
	y2 = replaceInYAML(y2, "model_class: build\n", "model_class: heavy\n")
	y2 = replaceInYAML(y2, "cr_level: CR-A+CR-B\n", "cr_level: CR-B\n")
	if err := os.WriteFile(filepath.Join(dir, "T-1.yaml"), []byte(y1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "T-2.yaml"), []byte(y2), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func replaceInYAML(y, old, replacement string) string {
	for i := 0; i+len(old) <= len(y); i++ {
		if y[i:i+len(old)] == old {
			return y[:i] + replacement + y[i+len(old):]
		}
	}
	return y
}

func TestStatusBoardCommands(t *testing.T) {
	t.Run("RunStatus aggregates by weight and model class", testRunStatusAggregates)
	t.Run("RunBoard groups into sorted columns", testRunBoardGroups)
	t.Run("RunCommand dispatches both verbs and refuses a missing root", testStatusBoardRunCommand)
	t.Run("status/board never mutate the tree", testStatusBoardReadOnly)
}

// treeDigest hashes every file under root by relative path.
func treeDigest(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// testStatusBoardReadOnly proves status/board never write: it runs both
// verbs through RunCommand and DispatchTool, then hashes every file
// before/after.
func testStatusBoardReadOnly(t *testing.T) {
	ctx := context.Background()
	root := mkStatusFixture(t)
	before := treeDigest(t, root)
	h := handlers{}
	input, _ := json.Marshal(statusRequest{Root: root, Phase: "P1"})
	if err := h.RunCommand(ctx, statusCommandName, []string{root, "P1"}); err != nil {
		t.Fatalf("RunCommand status: %v", err)
	}
	if err := h.RunCommand(ctx, boardCommandName, []string{root, "P1"}); err != nil {
		t.Fatalf("RunCommand board: %v", err)
	}
	if _, err := h.DispatchTool(ctx, "cascade_plugin_pbd_status", input); err != nil {
		t.Fatalf("DispatchTool status: %v", err)
	}
	if _, err := h.DispatchTool(ctx, "cascade_plugin_pbd_board", input); err != nil {
		t.Fatalf("DispatchTool board: %v", err)
	}
	after := treeDigest(t, root)
	if len(before) != len(after) {
		t.Fatalf("file count changed: before %d, after %d", len(before), len(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s mutated by a read-only status/board call", path)
		}
	}
}

func testRunStatusAggregates(t *testing.T) {
	root := mkStatusFixture(t)
	report, err := RunStatus(root, "P1")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if report.Total != 2 || report.ByWeight["S"] != 1 || report.ByWeight["M"] != 1 ||
		report.ByModelClass["build"] != 1 || report.ByModelClass["heavy"] != 1 {
		t.Errorf("report = %+v, want Total 2, one S/build, one M/heavy", report)
	}
}

func testRunBoardGroups(t *testing.T) {
	root := mkStatusFixture(t)
	report, err := RunBoard(root, "P1")
	if err != nil {
		t.Fatalf("RunBoard: %v", err)
	}
	if len(report.Columns) != 2 || report.Columns[0].ModelClass != "build" || report.Columns[1].ModelClass != "heavy" {
		t.Fatalf("Columns = %+v, want [build, heavy] sorted", report.Columns)
	}
	if len(report.Columns[0].Tickets) != 1 || report.Columns[0].Tickets[0].CanonicalID != "P1-E14-W3-S29-T1" {
		t.Errorf("build column = %+v", report.Columns[0].Tickets)
	}
}

func testStatusBoardRunCommand(t *testing.T) {
	root := mkStatusFixture(t)
	h := handlers{}
	if err := h.RunCommand(context.Background(), statusCommandName, []string{root, "P1"}); err != nil {
		t.Errorf("RunCommand status: %v", err)
	}
	if err := h.RunCommand(context.Background(), boardCommandName, []string{root, "P1"}); err != nil {
		t.Errorf("RunCommand board: %v", err)
	}
	if err := h.RunCommand(context.Background(), statusCommandName, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RunCommand status (no root): err = %v, want KindInvalidInput", err)
	}
}

// TestStatusBoardMirroredMethods proves the manifest declares the
// mirrored RPC method and MCP tool names.
func TestStatusBoardMirroredMethods(t *testing.T) {
	m := manifest()
	rpcByName := map[string]string{}
	for _, c := range m.Provides.Commands {
		rpcByName[c.Name] = c.RPCMethod
	}
	if rpcByName[statusCommandName] != "plugin.pbd.status" || rpcByName[boardCommandName] != "plugin.pbd.board" {
		t.Errorf("RPCMethod map = %+v, want plugin.pbd.status/plugin.pbd.board", rpcByName)
	}
	tools := map[string]bool{}
	for _, ts := range m.Provides.Tools {
		tools[ts.Name] = true
	}
	if !tools["cascade_plugin_pbd_status"] || !tools["cascade_plugin_pbd_board"] {
		t.Errorf("tools = %+v, want cascade_plugin_pbd_status and cascade_plugin_pbd_board", tools)
	}
}

// TestStatusBoardRealCounterparts is Art.2's requirement: real
// rpc.Registry/rpc.Parse and mcp.ToolRegistry clients, never self-authored.
func TestStatusBoardRealCounterparts(t *testing.T) {
	t.Run("a real JSON-RPC client dispatches plugin.pbd.status", testRealJSONRPCClient)
	t.Run("a real MCP client calls the status tool through DispatchTool", testRealMCPClient)
}

func testRealJSONRPCClient(t *testing.T) {
	root := mkStatusFixture(t)
	reg := rpc.NewRegistry()
	reg.Register(statusRPCMethod, StatusRPC)
	body := []byte(`{"jsonrpc":"2.0","method":"plugin.pbd.status","params":{"root":"` +
		jsonEscape(root) + `","phase":"P1"},"id":1}`)
	req, perr := rpc.Parse(body)
	if perr != nil {
		t.Fatalf("rpc.Parse: %+v", perr)
	}
	result, derr := reg.Dispatch(context.Background(), req)
	if derr != nil {
		t.Fatalf("Dispatch: %+v", derr)
	}
	report, ok := result.(StatusReport)
	if !ok || report.Total != 2 {
		t.Errorf("result = %+v, ok=%v, want a StatusReport with Total 2", result, ok)
	}
}

func testRealMCPClient(t *testing.T) {
	root := mkStatusFixture(t)
	registry := mcp.NewToolRegistry(func() []plugin.BuiltinRegistration { return plugin.Builtins() })
	found := false
	for _, tool := range registry.List() {
		found = found || tool.Name == "cascade_plugin_pbd_status"
	}
	if !found {
		t.Fatal("cascade_plugin_pbd_status not in the policy-filtered tool list")
	}
	input, _ := json.Marshal(statusRequest{Root: root, Phase: "P1"})
	out, err := registry.Call(context.Background(), "cascade_plugin_pbd_status", input)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var report StatusReport
	if err := json.Unmarshal(out, &report); err != nil || report.Total != 2 {
		t.Errorf("report = %+v, err = %v, want Total 2", report, err)
	}
}

// jsonEscape escapes s for a hand-built JSON string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// TestReadRowsFromStore proves the literal T1-projected-state read path
// against a real pkg/provider.Store (providers/sqlite.Open, Art.2).
func TestReadRowsFromStore(t *testing.T) {
	root := mkStatusFixture(t)
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	proj, err := pews.NewProjector(pews.ProjectorOptions{Store: store, Clock: statusFixedClock{}})
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := proj.Project(context.Background(), root, "P1"); err != nil {
		t.Fatalf("Project: %v", err)
	}
	rows, err := ReadRowsFromStore(context.Background(), store)
	if err != nil {
		t.Fatalf("ReadRowsFromStore: %v", err)
	}
	if len(rows) != 2 || rows[0].CanonicalID != "P1-E14-W3-S29-T1" || rows[1].CanonicalID != "P1-E14-W3-S29-T2" {
		t.Errorf("rows = %+v, want the two projected tickets sorted by canonical id", rows)
	}
	if report := BuildStatusReport("P1", rows); report.Total != 2 {
		t.Errorf("BuildStatusReport over store rows: Total = %d, want 2", report.Total)
	}
}

// TestStatusBoardErrorPaths table-drives the read/decode refusals.
func TestStatusBoardErrorPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cases := []struct {
		name string
		fn   func() error
		want cascade.Kind
	}{
		{"RunStatus missing root", func() error { _, e := RunStatus(missing, "P1"); return e }, cascade.KindNotFound},
		{"RunBoard missing root", func() error { _, e := RunBoard(missing, "P1"); return e }, cascade.KindNotFound},
		{"decode nil", func() error { _, e := decodeStatusRequest(nil); return e }, cascade.KindInvalidInput},
		{"decode malformed", func() error { _, e := decodeStatusRequest([]byte("not json")); return e }, cascade.KindInvalidInput},
		{"decode no root", func() error { _, e := decodeStatusRequest([]byte(`{"phase":"P1"}`)); return e }, cascade.KindInvalidInput},
		{"StatusRPC nil params", func() error { _, e := StatusRPC(context.Background(), nil); return e }, cascade.KindInvalidInput},
		{"BoardRPC nil params", func() error { _, e := BoardRPC(context.Background(), nil); return e }, cascade.KindInvalidInput},
		{"DispatchTool unknown", func() error {
			_, e := (handlers{}).DispatchTool(context.Background(), "not-a-real-tool", nil)
			return e
		}, cascade.KindUnsupported},
	}
	for _, c := range cases {
		if err := c.fn(); !cascade.HasKind(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// TestStatusBoardPlatformParity: nothing in status.go is platform-gated,
// so Art.5 is satisfied by running unconditionally rather than a skip.
func TestStatusBoardPlatformParity(t *testing.T) {
	root := mkStatusFixture(t)
	if _, err := RunStatus(root, "P1"); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if _, err := RunBoard(root, "P1"); err != nil {
		t.Fatalf("RunBoard: %v", err)
	}
}
