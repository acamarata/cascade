package generate

// Purpose: the report-only location lint. Every generator writes into a
//   closed list of locations; a manifest entry anywhere else is named.
// Inputs: a Manifest.
// Outputs: one DriftItem per entry outside GeneratedLocations, by path.
// Constraints: report only. LintLocations reads nothing from disk and never
//   moves, rewrites or deletes a file.
// SPORT: context-engine/policy-lint (ADD, P1-GEN-06).

import (
	"path"
	"sort"
)

// condOutsideLocations is the condition LintLocations reports.
const condOutsideLocations = "outside-generated-locations"

// generatedLocations is the closed list, as path.Match patterns.
var generatedLocations = []string{
	".claude/CLAUDE.md",
	".claude/rules/*.md",
	".claude/agents/*.md",
	".claude/skills/*/SKILL.md",
	".claude/workflows/*.js",
	".claude/workflows/cascade-workflows.json",
	"AGENTS.md",
	".codex/config.toml",
	"opencode.json",
	".cascade/policy-kernel.md",
}

// GeneratedLocations returns a copy of the closed list of locations a
// generator may write, as path.Match patterns over slash paths.
func GeneratedLocations() []string {
	return append([]string(nil), generatedLocations...)
}

// LintLocations names every manifest entry outside GeneratedLocations with
// the condition "outside-generated-locations". It only reports.
func LintLocations(m Manifest) []DriftItem {
	var out []DriftItem
	for _, e := range m.Entries {
		if !inGeneratedLocations(e.Path) {
			out = append(out, DriftItem{Path: e.Path, Condition: condOutsideLocations})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// inGeneratedLocations reports whether rel matches a closed location.
func inGeneratedLocations(rel string) bool {
	for _, p := range generatedLocations {
		if ok, err := path.Match(p, rel); err == nil && ok {
			return true
		}
	}
	return false
}
