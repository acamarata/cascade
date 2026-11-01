package generate

// Purpose: RenderScopedHarness, the scoped AGENTS.md, Codex and OpenCode
//   generator over a policy View. Each ScopeView becomes one scoped
//   MergedContext, rendered through the unmodified CX and OC instruction
//   writers into one agents-md-<slug> block of the repository-root
//   AGENTS.md (one shared managed file), plus <scope>/.codex/config.toml
//   and <scope>/opencode.json carrying only the owned MCP entries.
// Inputs: a View from BuildView.
// Outputs: the Rendered files, in a fixed order: AGENTS.md, then per scope
//   .codex/config.toml and opencode.json.
// Constraints: the published owned keys (contract:scoped-harness-generation)
//   are the only keys ever written: [mcp_servers.cascade] in the
//   codex-config block and the "cascade" member of the top-level "mcp"
//   object in the opencode-config block. A later projector adds its own
//   block through Writer.ApplyBlock; keys outside every block stay hand
//   content. Both writers are called only through Generate; when their
//   outputs differ the render is KindInternal. No clock.
// SPORT: context-engine/scoped-harness-generation (ADD, P1-GEN-10).

import (
	"bytes"
	pathpkg "path"
	"strings"

	cctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The owned block ids of the two config files.
const (
	// CodexConfigBlock is the block id of the owned .codex/config.toml table.
	CodexConfigBlock = "codex-config"
	// OpenCodeConfigBlock is the block id of the owned opencode.json member.
	OpenCodeConfigBlock = "opencode-config"
)

// The generator ids RenderScopedHarness records, and the AGENTS.md target.
const (
	genAgentsMD       = "agents-md"
	genCodexConfig    = "codex-config"
	genOpenCodeConfig = "opencode-config"
	agentsMDTarget    = "AGENTS.md"
	agentsBlockPrefix = "agents-md-"
)

// The section headings of a scoped MergedContext.
const (
	headScopeCommands = "## Commands"
	headScopeFacts    = "## Accepted facts"
)

// MCPServeArgv is the argv both config files run: the stdio MCP server.
// Each call returns a fresh slice.
func MCPServeArgv() []string {
	return []string{"cascade", "mcp", "serve", "--stdio"}
}

// RenderScopedHarness renders AGENTS.md and every scope's Codex and
// OpenCode config for v. Equal views render equal bytes. A view with no
// scope, a bad or repeated slug or a Rel that is not a clean relative path
// is KindInvalidInput.
func RenderScopedHarness(v View) ([]Rendered, error) {
	if len(v.Scopes) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "generate: policy view has no scope")
	}
	var agents []byte
	var configs []Rendered
	seen := map[string]bool{}
	for _, s := range v.Scopes {
		if err := checkScope(s, seen); err != nil {
			return nil, err
		}
		body, err := renderAgentsBody(v, s)
		if err != nil {
			return nil, err
		}
		agents = append(agents, WrapBlock(FormMarkdown, agentsBlockPrefix+s.Slug, body)...)
		configs = append(configs, renderCodexConfig(s.Rel), renderOpenCodeConfig(s.Rel))
	}
	out := []Rendered{{Path: agentsMDTarget, GeneratorID: genAgentsMD, Content: agents}}
	return append(out, configs...), nil
}

// checkScope validates one scope and records its slug in seen.
func checkScope(s ScopeView, seen map[string]bool) error {
	if s.Slug != ScopeSlug(s.Rel) {
		return cascade.Newf(cascade.KindInvalidInput,
			"generate: scope %q has slug %q, want %q", s.Rel, s.Slug, ScopeSlug(s.Rel))
	}
	if !blockIDPattern.MatchString(agentsBlockPrefix + s.Slug) {
		return cascade.Newf(cascade.KindInvalidInput, "generate: scope slug %q makes no valid block id", s.Slug)
	}
	if seen[s.Slug] {
		return cascade.Newf(cascade.KindInvalidInput, "generate: two scopes share slug %q", s.Slug)
	}
	seen[s.Slug] = true
	clean := pathpkg.Clean(s.Rel)
	if s.Rel != clean || pathpkg.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, `\`) {
		return cascade.Newf(cascade.KindInvalidInput, "generate: scope %q is not a clean relative path", s.Rel)
	}
	return nil
}

// scopedPath joins a scope's Rel and a file name.
func scopedPath(rel, name string) string {
	if rel == "." {
		return name
	}
	return rel + "/" + name
}

// renderAgentsBody renders s through both AGENTS.md writers and returns
// their one shared file's bytes.
func renderAgentsBody(v View, s ScopeView) ([]byte, error) {
	mc := scopedMerged(v, s)
	cx, err := oneAgentsFile(&cctx.CXInstructionWriter{}, mc)
	if err != nil {
		return nil, err
	}
	oc, err := oneAgentsFile(&cctx.OCInstructionWriter{}, mc)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(cx, oc) {
		return nil, cascade.Newf(cascade.KindInternal, "generate: CX and OC writers disagree on AGENTS.md for scope %q", s.Rel)
	}
	id := agentsBlockPrefix + s.Slug
	if err := refuseMarkerBody(FormMarkdown, id, cx); err != nil {
		return nil, err
	}
	return cx, nil
}

// oneAgentsFile runs w.Generate and requires exactly one AGENTS.md.
func oneAgentsFile(w cctx.HarnessGenerator, mc cctx.MergedContext) ([]byte, error) {
	files, err := w.Generate(mc)
	if err != nil {
		return nil, err
	}
	if len(files) != 1 || files[0].Name != agentsMDTarget {
		return nil, cascade.Newf(cascade.KindInternal, "generate: AGENTS.md writer rendered %d files, want one %s", len(files), agentsMDTarget)
	}
	return files[0].Content, nil
}

// scopeRole is the tier a scope's MergedContext speaks for: the repository
// root is the PRC tier, every directory below it a PAC tier.
func scopeRole(s ScopeView) cctx.TierRole {
	if s.Rel == "." {
		return cctx.TierPRC
	}
	return cctx.TierPAC
}

// scopedMerged is the MergedContext of one scope: the repository
// invariants, its commands and its accepted facts, nothing from any tier
// outside the repository.
func scopedMerged(v View, s ScopeView) cctx.MergedContext {
	role := scopeRole(s)
	ord := int(role) - 1
	inv := headInvariants + "\n\n" +
		"- Blocks between `cascade:managed` markers are generated; edit outside them, never inside.\n" +
		"- Scope: " + inlineCode(s.Rel) + ".\n" +
		"- Continuous integration: " + ciList(v.CI) + "."
	var cmds, facts strings.Builder
	cmds.WriteString(headScopeCommands + "\n")
	if len(s.Languages) == 0 {
		cmds.WriteString("\n- No language detected.")
	}
	for _, l := range s.Languages {
		cmds.WriteString("\n- " + inlineCode(l.Language) + ": " + commandList(l))
	}
	facts.WriteString(headScopeFacts + "\n")
	if len(s.Facts) == 0 {
		facts.WriteString("\n- None beyond the detected inventory.")
	}
	for _, f := range s.Facts {
		facts.WriteString("\n- " + inlineCode(string(f.Subject)) + " (" + inlineCode(string(f.Truth)) + "): " + inlineCode(f.Value))
	}
	secs := []cctx.MergedSection{
		{Heading: headInvariants, Content: inv, Role: role, Ordinal: ord},
		{Heading: headScopeCommands, Content: cmds.String(), Role: role, Ordinal: ord},
		{Heading: headScopeFacts, Content: facts.String(), Role: role, Ordinal: ord},
	}
	prov := make(map[string]cctx.TierRole, len(secs))
	for _, sec := range secs {
		prov[sec.Heading] = role
	}
	return cctx.MergedContext{Sections: secs, Provenance: prov}
}
