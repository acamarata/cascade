package context

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Purpose: the CC managed-block renderer. Turns one tier's surviving
//   MergedSections into the exact bytes that go between the cascade markers
//   in that tier's CLAUDE.md, and refuses to emit content that would leak a
//   machine-specific path or a secret-shaped value into a committed file.
// Inputs: a TierRole and that tier's MergedSections, in merge order.
// Outputs: the managed block's bytes, plus the headings that were held back.
// Constraints: pure and deterministic. No clock, no filesystem, no map
//   iteration on the output path. Header wording is v1's verbatim
//   (cascade-cli generate_instructions/cc.rs at archive/p9-integration)
//   apart from the CLI-fallback line ratified by R-16.43.
// SPORT: context-engine/cc-instruction-gen (ADD, per T-3 sport_updates).

// mcpServerRef is the MCP server reference the header points at. It is a
// transport spelling, not a path: nothing here may carry a directory from
// the machine that ran the generator.
const mcpServerRef = "stdio: cascade mcp stdio"

// cliFallbackLine is the single v2 delta from v1's harvested header
// (R-16.43). v1's post-mortem found the MCP server could be dead while the
// instruction still told the harness to call it, so the harness did nothing
// at all. Naming the daemonless CLI path in the same breath makes an MCP
// outage a degradation instead of a silent no-op.
const cliFallbackLine = "If the cascade MCP tools are unavailable, run `cascade recall` and " +
	"`cascade context slice` through Bash instead."

// tierDescriptions is the human-readable tier name the header prints,
// worded from tier.go's own godoc for each role. Indexed by TierRole.
var tierDescriptions = [...]string{
	"",
	"Global Cascade Instructions",
	"All-Sites Instructions",
	"Per-Project Instructions",
	"Per-Repo Instructions",
	"Per-App Instructions",
}

// preambleLabel names a heading-less block in the exclusion notice. A
// preamble has no heading to print, and printing nothing would make the
// notice read as though a section had vanished without being named.
const preambleLabel = "(preamble)"

// renderTierBlock renders role's managed block from its sections.
//
// Every section is either emitted or named in the exclusion notice; there is
// no third outcome. A tier all of whose sections are held back still renders
// a block, carrying the notice and nothing else, because a file that
// silently shrinks to a header is indistinguishable from one whose tier went
// missing.
func renderTierBlock(role TierRole, sections []MergedSection) string {
	var kept []string
	var excluded []string
	for _, s := range sections {
		if reason := unrenderable(s.Content); reason != "" {
			excluded = append(excluded, sectionLabel(s)+" ("+reason+")")
			continue
		}
		if body := strings.TrimRight(s.Content, "\n"); body != "" {
			kept = append(kept, body)
		}
	}

	parts := []string{tierHeader(role)}
	if len(excluded) > 0 {
		parts = append(parts, exclusionNotice(excluded))
	}
	parts = append(parts, kept...)

	body := strings.Join(parts, "\n\n") + "\n\n" + markerClose
	return markerOpenPrefix + digestAttr + bodyDigest(body) + " -->\n" + body
}

// tierHeader is the fixed preamble of every CC managed block: what the tier
// is, where the MCP server lives, what to call, and what to do when the call
// is not available.
func tierHeader(role TierRole) string {
	desc := ""
	if role.Valid() {
		desc = tierDescriptions[role]
	}
	return "## Cascade Context — " + role.String() + " Tier (" + desc + ")\n" +
		"\n" +
		"**MCP server:** `" + mcpServerRef + "`\n" +
		"\n" +
		"Call `cascade.search` before responding to queries about this project.\n" +
		"Call `cascade.context_slice` to retrieve relevant context from the RAG index.\n" +
		cliFallbackLine
}

// exclusionNotice renders the block that names everything held back. It is a
// blockquote so a harness reading the file treats it as commentary rather
// than as an instruction, and it names headings only: the offending text is
// never echoed, since echoing it is the leak the exclusion exists to stop.
func exclusionNotice(labels []string) string {
	noun := "section"
	if len(labels) != 1 {
		noun = "sections"
	}
	var b strings.Builder
	b.WriteString("> cascade: " + strconv.Itoa(len(labels)) + " " + noun +
		" from this tier could not be rendered and were left out of this file.\n")
	b.WriteString("> Fix the source instruction file, then regenerate. Held back:\n")
	for _, l := range labels {
		b.WriteString(">   - " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// sectionLabel names a section for the exclusion notice.
func sectionLabel(s MergedSection) string {
	if s.Heading == "" {
		return preambleLabel
	}
	return strconv.Quote(s.Heading)
}

// unrenderableRule pairs a detector with the reason printed when it fires.
type unrenderableRule struct {
	reason string
	re     *regexp.Regexp
}

// unrenderableRules are the fail-closed content checks. A generated
// instruction file is committed to a repository and read by other people, so
// content that names one machine's directories, or that looks like a
// credential, does not go into it. The rules are ordered, and the first
// match wins, so the reported reason is stable for a given input.
//
// These deliberately over-match rather than under-match. A false positive
// costs one named, visible exclusion the author can see and fix; a false
// negative commits somebody's token to a public repository.
var unrenderableRules = []unrenderableRule{
	{"machine-specific path", regexp.MustCompile(
		`(?:^|[\s"'` + "`" + `(<=])(?:/Users/|/home/|/Volumes/|/root/|/private/var/folders/|[A-Za-z]:\\\\?[Uu]sers)`)},
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"credential-shaped token", regexp.MustCompile(
		`(?:sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,})`)},
	{"assigned secret value", regexp.MustCompile(
		`(?i)\b(?:api[_-]?key|secret|token|password|passwd|bearer)\b\s*[:=]\s*["'` + "`" + `]?[A-Za-z0-9+/_-]{12,}`)},
}

// unrenderable reports why content must not be written into a generated
// file, or "" when it is safe to emit.
func unrenderable(content string) string {
	for _, r := range unrenderableRules {
		if r.re.MatchString(content) {
			return r.reason
		}
	}
	return ""
}

// groupByRole buckets mc's sections by contributing tier and returns the
// roles present, in ascending ordinal order.
//
// The sort is on the ordinal carried by the sections themselves rather than
// on the role's numeric value, so the emission order is the merge's own
// order and cannot drift from it. Iterating the bucket map directly would
// randomize the output; that is the whole reason this returns a sorted
// slice.
func groupByRole(mc MergedContext) ([]TierRole, map[TierRole][]MergedSection) {
	buckets := make(map[TierRole][]MergedSection, len(mc.Sections))
	ordinals := make(map[TierRole]int, len(mc.Sections))
	for _, s := range mc.Sections {
		if _, seen := buckets[s.Role]; !seen {
			ordinals[s.Role] = s.Ordinal
		}
		buckets[s.Role] = append(buckets[s.Role], s)
	}
	roles := make([]TierRole, 0, len(buckets))
	for r := range buckets {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool {
		if ordinals[roles[i]] != ordinals[roles[j]] {
			return ordinals[roles[i]] < ordinals[roles[j]]
		}
		return roles[i] < roles[j]
	})
	return roles, buckets
}
