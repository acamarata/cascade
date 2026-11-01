package generate

// Purpose: the Codex leg of RenderScopedHarness: <scope>/.codex/config.toml
//   holding exactly the owned [mcp_servers.cascade] table inside the
//   codex-config block.
// Inputs: a scope's Rel.
// Outputs: one Rendered file.
// Constraints: the block holds the published owned keys and nothing else;
//   every byte of the file is inside the block, so a person's tables go
//   outside it and a projector's keys go into its own block. No clock.
// SPORT: context-engine/scoped-harness-generation (ADD, P1-GEN-10).

import (
	"strconv"
	"strings"
)

// codexConfigName is the Codex project config, relative to its scope.
const codexConfigName = ".codex/config.toml"

// renderCodexConfig renders the Codex config of the scope at rel.
func renderCodexConfig(rel string) Rendered {
	return Rendered{
		Path:        scopedPath(rel, codexConfigName),
		GeneratorID: genCodexConfig,
		Content:     WrapBlock(FormHash, CodexConfigBlock, []byte(codexConfigBody())),
	}
}

// codexConfigBody is the owned TOML table.
func codexConfigBody() string {
	argv := MCPServeArgv()
	return "[mcp_servers.cascade]\n" +
		"command = " + tomlString(argv[0]) + "\n" +
		"args = [" + tomlStrings(argv[1:]) + "]\n"
}

// tomlString renders s as a TOML basic string. The argv is plain ASCII, for
// which Go's quoting and TOML's agree.
func tomlString(s string) string { return strconv.Quote(s) }

// tomlStrings renders ss as the members of a one-line TOML array.
func tomlStrings(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = tomlString(s)
	}
	return strings.Join(parts, ", ")
}
