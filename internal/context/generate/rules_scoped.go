package generate

// Purpose: the path-scoped half of RenderRules. Each ScopeView becomes one
//   .claude/rules/<slug>.md holding one block, rules-<slug>, with the
//   per-language commands and the accepted facts CLAUDE.md leaves out.
// Inputs: one ScopeView.
// Outputs: one Rendered file; KindInvalidInput for a bad slug.
// Constraints: every view value is inline code; the body is refused when
//   it holds a marker-shaped line. No clock.
// SPORT: context-engine/rules-generator (ADD, P1-GEN-06).

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rulesDir is where the scoped rules files live.
const rulesDir = ".claude/rules/"

// renderScoped renders the rules file for s.
func renderScoped(s ScopeView) (Rendered, error) {
	if s.Slug != ScopeSlug(s.Rel) {
		return Rendered{}, cascade.Newf(cascade.KindInvalidInput,
			"generate: scope %q has slug %q, want %q", s.Rel, s.Slug, ScopeSlug(s.Rel))
	}
	id := "rules-" + s.Slug
	if !blockIDPattern.MatchString(id) {
		return Rendered{}, cascade.Newf(cascade.KindInvalidInput, "generate: scope slug %q makes no valid block id", s.Slug)
	}
	body := []byte(scopedBody(s))
	if err := refuseMarkerBody(FormMarkdown, id, body); err != nil {
		return Rendered{}, err
	}
	return Rendered{Path: rulesDir + s.Slug + ".md", GeneratorID: genRulesScoped, Content: WrapBlock(FormMarkdown, id, body)}, nil
}

// scopedBody is the Markdown inside the rules-<slug> block.
func scopedBody(s ScopeView) string {
	var b strings.Builder
	b.WriteString("# Path rules for " + inlineCode(s.Rel) + "\n\n## Commands\n\n")
	if len(s.Languages) == 0 {
		b.WriteString("- No language detected.\n")
	}
	for _, l := range s.Languages {
		b.WriteString("- " + inlineCode(l.Language) + ": " + commandList(l) + "\n")
	}
	b.WriteString("\n## Accepted facts\n\n")
	if len(s.Facts) == 0 {
		b.WriteString("- None beyond the detected inventory.\n")
	}
	for _, f := range s.Facts {
		b.WriteString("- " + inlineCode(string(f.Subject)) + " (" + inlineCode(string(f.Truth)) + "): " + inlineCode(f.Value) + "\n")
	}
	return b.String()
}

// commandList names a language's detected commands, or says there are none.
func commandList(l LanguageView) string {
	var parts []string
	for _, c := range [...]struct{ name, cmd string }{{"build", l.Build}, {"test", l.Test}, {"lint", l.Lint}} {
		if c.cmd != "" {
			parts = append(parts, c.name+" "+inlineCode(c.cmd))
		}
	}
	if len(parts) == 0 {
		return "no commands detected"
	}
	return strings.Join(parts, "; ")
}
