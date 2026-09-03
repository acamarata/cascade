package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/plugin"

	_ "github.com/acamarata/cascade/plugins/examples/example-builtin"
)

// fakeMCPDeps builds mcpDeps whose Serve* funcs are recorded, never real
// I/O — this file never binds a real socket or reads a real pipe, keeping
// it in the no-network default test lane (no `net` import).
func fakeMCPDeps() (*mcpDeps, *[]string) {
	var calls []string
	deps := &mcpDeps{
		NewTools: func() *mcp.ToolRegistry { return mcp.NewToolRegistry(plugin.Builtins) },
		ServeStdio: func(context.Context, io.Reader, io.Writer) error {
			calls = append(calls, "stdio")
			return nil
		},
		ServeSocket: func(context.Context) error { calls = append(calls, "socket"); return nil },
		StdinIsPipe: func() bool { return true },
	}
	return deps, &calls
}

func TestMCPCommand_ServeDefaultsToStdioWhenPiped(t *testing.T) {
	deps, calls := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	cmd.SetArgs([]string{"serve"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != "stdio" {
		t.Fatalf("calls = %v, want [stdio]", *calls)
	}
}

func TestMCPCommand_ServeSocketFlag(t *testing.T) {
	deps, calls := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	cmd.SetArgs([]string{"serve", "--socket"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != "socket" {
		t.Fatalf("calls = %v, want [socket]", *calls)
	}
}

func TestMCPCommand_ServeStdioFlag(t *testing.T) {
	deps, calls := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	cmd.SetArgs([]string{"serve", "--stdio"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != "stdio" {
		t.Fatalf("calls = %v, want [stdio]", *calls)
	}
}

func TestMCPCommand_ServeMutuallyExclusiveFlags(t *testing.T) {
	deps, _ := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	cmd.SetArgs([]string{"serve", "--stdio", "--socket"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error for --stdio and --socket together")
	}
}

// TestMCPCommand is the contract's required cmd/cascade/mcp_test.go
// coverage: `cascade mcp tools list --json` prints the policy-filtered
// registry without a running daemon, driven end to end through the real
// example-builtin plugin (C-S05.T6).
func TestMCPCommand(t *testing.T) {
	deps, _ := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.PersistentFlags().Bool("quiet", false, "")
	cmd.PersistentFlags().Bool("verbose", false, "")
	cmd.PersistentFlags().Bool("no-color", false, "")
	cmd.SetArgs([]string{"tools", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "greet-tool") {
		t.Fatalf("tools list --json output = %s, want it to contain greet-tool", out.String())
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("output not valid --json envelope: %v (%s)", err, out.String())
	}
	if !envelope.OK {
		t.Fatal("envelope.OK = false, want true")
	}
}

func TestNewMCPCmd_MountsServeAndTools(t *testing.T) {
	deps, _ := fakeMCPDeps()
	cmd := newMCPCmd(*deps)
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	if !names["serve"] || !names["tools"] {
		t.Fatalf("mcp command tree = %v, want serve and tools mounted", names)
	}
}
