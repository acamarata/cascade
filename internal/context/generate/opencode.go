package generate

// Purpose: the OpenCode leg of RenderScopedHarness: <scope>/opencode.json
//   holding exactly the owned "cascade" member of the top-level "mcp"
//   object inside the opencode-config block.
// Inputs: a scope's Rel.
// Outputs: one Rendered file.
// Constraints: the block holds the published owned member and nothing
//   else; the only bytes outside it are the braces of the root and "mcp"
//   objects, so a person's keys go beside them. OpenCode reads the file
//   as JSONC, so the // markers are comments to it. No clock.
// SPORT: context-engine/scoped-harness-generation (ADD, P1-GEN-10).

import (
	"strconv"
	"strings"
)

// openCodeConfigName is the OpenCode project config, relative to its scope.
const openCodeConfigName = "opencode.json"

// The bytes around the block: the root object and its "mcp" object.
const (
	openCodeHead = "{\n  \"mcp\": {\n"
	openCodeTail = "  }\n}\n"
	openCodePad  = "    "
)

// renderOpenCodeConfig renders the OpenCode config of the scope at rel.
func renderOpenCodeConfig(rel string) Rendered {
	block := WrapBlock(FormSlash, OpenCodeConfigBlock, []byte(openCodeMember()))
	var b strings.Builder
	b.WriteString(openCodeHead)
	for _, line := range strings.SplitAfter(string(block), "\n") {
		if line != "" {
			b.WriteString(openCodePad + line)
		}
	}
	b.WriteString(openCodeTail)
	return Rendered{Path: scopedPath(rel, openCodeConfigName), GeneratorID: genOpenCodeConfig, Content: []byte(b.String())}
}

// openCodeMember is the owned "cascade" member, on one line.
func openCodeMember() string {
	argv := MCPServeArgv()
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = strconv.Quote(a)
	}
	return `"cascade": {"type": "local", "command": [` + strings.Join(quoted, ", ") + `], "enabled": true}` + "\n"
}
