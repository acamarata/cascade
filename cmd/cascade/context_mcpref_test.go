package main

import (
	"strings"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
)

// TestGeneratedMCPServerRefResolves proves the `**MCP server:**` line the CC
// instruction writer renders names a command the real root resolves: the
// argv is parsed back out of a rendered file (not compared to a literal),
// root.Find must return the `mcp serve` command, and that command must
// declare the --stdio flag the line passes.
func TestGeneratedMCPServerRefResolves(t *testing.T) {
	mc, err := cascadecontext.MergeTiers([]cascadecontext.TierRecord{{
		Role: cascadecontext.TierPRC, Ordinal: 3, Content: "## Style\n\nShort sentences.\n",
	}})
	if err != nil {
		t.Fatalf("MergeTiers: %v", err)
	}
	files, err := (&cascadecontext.CCInstructionWriter{}).Generate(mc)
	if err != nil || len(files) != 1 {
		t.Fatalf("Generate: %d files, err %v", len(files), err)
	}
	const prefix = "**MCP server:** `stdio: "
	var argv []string
	for _, line := range strings.Split(string(files[0].Content), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			argv = strings.Fields(strings.TrimSuffix(rest, "`"))
		}
	}
	if len(argv) < 2 || argv[0] != "cascade" {
		t.Fatalf("no cascade command in the rendered MCP line: %q", argv)
	}
	root := newRootCmd()
	cmd, rest, err := root.Find(argv[1:])
	if err != nil {
		t.Fatalf("root.Find(%q): %v", argv[1:], err)
	}
	if cmd.Name() != "serve" || cmd.Parent() == nil || cmd.Parent().Name() != "mcp" {
		t.Fatalf("root.Find(%q) resolved to %q, want `mcp serve`", argv[1:], cmd.CommandPath())
	}
	if len(rest) != 1 || rest[0] != "--stdio" {
		t.Fatalf("unresolved argv after the command: %q, want only --stdio", rest)
	}
	if cmd.Flags().Lookup("stdio") == nil {
		t.Fatal("`mcp serve` declares no --stdio flag")
	}
}
