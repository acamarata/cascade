package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/context/generate"
)

// scopedHarnessArgvs parses the MCP argv back out of the rendered Codex
// and OpenCode config files, the way each harness reads them.
func scopedHarnessArgvs(t *testing.T) map[string][]string {
	t.Helper()
	rs, err := generate.RenderScopedHarness(generate.View{Scopes: []generate.ScopeView{{Slug: "root", Rel: "."}}})
	if err != nil {
		t.Fatalf("RenderScopedHarness: %v", err)
	}
	out := map[string][]string{}
	for _, r := range rs {
		switch r.Path {
		case ".codex/config.toml":
			var cfg struct {
				MCPServers map[string]struct {
					Command string   `toml:"command"`
					Args    []string `toml:"args"`
				} `toml:"mcp_servers"`
			}
			if err := toml.Unmarshal(r.Content, &cfg); err != nil {
				t.Fatalf("config.toml: %v", err)
			}
			s := cfg.MCPServers["cascade"]
			out[r.Path] = append([]string{s.Command}, s.Args...)
		case "opencode.json":
			var kept []string
			for _, line := range strings.Split(string(r.Content), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "//") {
					kept = append(kept, line)
				}
			}
			var cfg struct {
				MCP map[string]struct {
					Command []string `json:"command"`
				} `json:"mcp"`
			}
			if err := json.Unmarshal([]byte(strings.Join(kept, "\n")), &cfg); err != nil {
				t.Fatalf("opencode.json: %v", err)
			}
			out[r.Path] = cfg.MCP["cascade"].Command
		}
	}
	if len(out) != 2 {
		t.Fatalf("rendered config files = %v, want config.toml and opencode.json", out)
	}
	return out
}

// TestScopedHarnessMCPArgvResolves proves both generated harness configs
// run a command the real root resolves: the argv is parsed out of each
// rendered file, root.Find must return `mcp serve`, and the only argument
// left is the --stdio flag that command declares.
func TestScopedHarnessMCPArgvResolves(t *testing.T) {
	for path, argv := range scopedHarnessArgvs(t) {
		if strings.Join(argv, " ") != strings.Join(generate.MCPServeArgv(), " ") {
			t.Fatalf("%s argv = %q, want MCPServeArgv %q", path, argv, generate.MCPServeArgv())
		}
		if len(argv) < 2 || argv[0] != "cascade" {
			t.Fatalf("%s runs no cascade command: %q", path, argv)
		}
		root := newRootCmd()
		cmd, rest, err := root.Find(argv[1:])
		if err != nil {
			t.Fatalf("%s: root.Find(%q): %v", path, argv[1:], err)
		}
		if cmd.Name() != "serve" || cmd.Parent() == nil || cmd.Parent().Name() != "mcp" {
			t.Fatalf("%s: root.Find(%q) resolved to %q, want `mcp serve`", path, argv[1:], cmd.CommandPath())
		}
		if len(rest) != 1 || rest[0] != "--stdio" {
			t.Fatalf("%s: unresolved argv after the command: %q, want only --stdio", path, rest)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatalf("%s: `mcp serve` rejects %q: %v", path, rest, err)
		}
		if f := cmd.Flags().Lookup("stdio"); f == nil || f.Value.String() != "true" {
			t.Fatalf("%s: --stdio did not select the stdio transport", path)
		}
	}
}
