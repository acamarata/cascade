package generate

// Purpose: RenderRules, the rules generator over a policy View, and its
//   .claude/CLAUDE.md half. CLAUDE.md holds two blocks: cc-invariants, the
//   unmodified CCInstructionWriter's output for a MergedContext of the
//   repository invariants and Cascade availability, and policy-kernel@1.
// Inputs: a View from BuildView.
// Outputs: the Rendered files, in a fixed order: .claude/CLAUDE.md, one
//   .claude/rules/<slug>.md per scope, .cascade/policy-kernel.md.
// Constraints: no clock and no per-path detail in CLAUDE.md; every View
//   value is emitted as inline code, never as Markdown structure; the
//   R-16.43 CLI fallback line is verbatim. This file registers no CLI verb
//   or RPC method.
// SPORT: context-engine/rules-generator (ADD, P1-GEN-06).

import (
	"strings"

	cctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The generator ids RenderRules records in the manifest.
const (
	genRulesCC      = "rules-cc"
	genRulesScoped  = "rules-scoped"
	genPolicyKernel = "policy-kernel"
)

// The CLAUDE.md block ids and target.
const (
	blockCCInvariants = "cc-invariants"
	blockPolicyKernel = "policy-kernel@" + policy.KernelVersion
	ccRulesTarget     = ".claude/CLAUDE.md"
)

// cliFallback is the R-16.43 line, verbatim.
const cliFallback = "if the Cascade MCP server is unavailable, use the `cascade` CLI for the same operations"

// The two section headings of the CLAUDE.md MergedContext.
const (
	headInvariants   = "## Repository invariants"
	headAvailability = "## Cascade availability"
)

// RenderRules renders every rules file for v. Equal views render equal
// bytes. A view with no scope, a scope whose slug is not ScopeSlug(Rel) or
// two scopes with one slug is KindInvalidInput.
func RenderRules(v View) ([]Rendered, error) {
	if len(v.Scopes) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "generate: policy view has no scope")
	}
	cc, err := renderCC(v)
	if err != nil {
		return nil, err
	}
	out := []Rendered{cc}
	seen := map[string]bool{}
	for _, s := range v.Scopes {
		if seen[s.Slug] {
			return nil, cascade.Newf(cascade.KindInvalidInput, "generate: two scopes share slug %q", s.Slug)
		}
		seen[s.Slug] = true
		r, err := renderScoped(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	kernel := Rendered{Path: provider.PolicyKernelFile, GeneratorID: genPolicyKernel, Content: policy.RenderKernelFile()}
	return append(out, kernel), nil
}

// renderCC renders .claude/CLAUDE.md.
func renderCC(v View) (Rendered, error) {
	files, err := (&cctx.CCInstructionWriter{}).Generate(ccMerged(v))
	if err != nil {
		return Rendered{}, err
	}
	if len(files) != 1 {
		return Rendered{}, cascade.Newf(cascade.KindInvalidInput, "generate: CC writer rendered %d files, want 1", len(files))
	}
	if err := refuseMarkerBody(FormMarkdown, blockCCInvariants, files[0].Content); err != nil {
		return Rendered{}, err
	}
	content := WrapBlock(FormMarkdown, blockCCInvariants, files[0].Content)
	content = append(content, WrapBlock(FormMarkdown, blockPolicyKernel, []byte(policy.KernelText))...)
	return Rendered{Path: ccRulesTarget, GeneratorID: genRulesCC, Content: content}, nil
}

// ccMerged is the MergedContext CLAUDE.md renders: the invariants and the
// Cascade availability sections of one repository-tier, nothing per path.
func ccMerged(v View) cctx.MergedContext {
	role := cctx.TierPRC
	ord := int(role) - 1
	inv := headInvariants + "\n\n" +
		"- Blocks between `cascade:managed` markers are generated; edit outside them, never inside.\n" +
		"- Path-scoped commands and facts live in `.claude/rules/`.\n" +
		"- The execution kernel below is also at `" + provider.PolicyKernelFile + "`.\n" +
		"- Continuous integration: " + ciList(v.CI) + "."
	avail := headAvailability + "\n\n" +
		"- Call `cascade.search` and `cascade.context_slice` before answering questions about this repository.\n" +
		"- " + cliFallback
	return cctx.MergedContext{
		Sections: []cctx.MergedSection{
			{Heading: headInvariants, Content: inv, Role: role, Ordinal: ord},
			{Heading: headAvailability, Content: avail, Role: role, Ordinal: ord},
		},
		Provenance: map[string]cctx.TierRole{headInvariants: role, headAvailability: role},
	}
}

// ciList names the detected CI systems as inline code, or `none`.
func ciList(ci repo.CIFacts) string {
	var names []string
	if ci.GitHubActions {
		names = append(names, inlineCode("github-actions"))
	}
	if ci.GitLabCI {
		names = append(names, inlineCode("gitlab-ci"))
	}
	if ci.CircleCI {
		names = append(names, inlineCode("circleci"))
	}
	if len(names) == 0 {
		return inlineCode("none")
	}
	return strings.Join(names, ", ")
}

// inlineCode renders s as one CommonMark code span. Line breaks and other
// control characters become spaces, and the fence is one backtick longer
// than the longest run inside, so no value can close the span, open a
// block or forge a marker line.
func inlineCode(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	if s == "" {
		return "` `"
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || strings.Trim(s, " ") == "" {
		s = " " + s + " "
	}
	return fence + s + fence
}
