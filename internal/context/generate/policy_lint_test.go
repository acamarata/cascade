package generate

import (
	"reflect"
	"testing"
)

// generatorOutputs is one path per location P1-GEN-06, -08, -09 and -10
// write.
var generatorOutputs = []string{
	".claude/CLAUDE.md",
	".claude/rules/root.md",
	".claude/rules/apps-web-ui.md",
	".claude/agents/cascade-implementer.md",
	".claude/skills/cascade-execution/SKILL.md",
	".claude/workflows/cascade-ship.js",
	".claude/workflows/cascade-workflows.json",
	"AGENTS.md",
	".codex/config.toml",
	"opencode.json",
	".cascade/policy-kernel.md",
}

func TestPolicyLintReportsOnly(t *testing.T) {
	root, _ := newRepo(t)
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	stray := Rendered{Path: "docs/notes.md", GeneratorID: "rules-scoped", Content: WrapBlock(FormMarkdown, "x", []byte("x\n"))}
	nested := Rendered{Path: ".claude/rules/deep/x.md", GeneratorID: "rules-scoped", Content: WrapBlock(FormMarkdown, "y", []byte("y\n"))}
	m, _, _ := applyAll(t, root, Manifest{}, false, append(rs, stray, nested))
	if err := WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	got := LintLocations(m)
	want := []DriftItem{
		{Path: ".claude/rules/deep/x.md", Condition: "outside-generated-locations"},
		{Path: "docs/notes.md", Condition: "outside-generated-locations"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LintLocations = %+v, want %+v", got, want)
	}
	sameTree(t, before, snapshot(t, root))
	if len(before) == 0 || missing(root, "docs/notes.md") {
		t.Fatal("the stray file is gone")
	}
}

func TestPolicyLintAcceptsEveryGeneratorOutput(t *testing.T) {
	isolate(t)
	if got := GeneratedLocations(); len(got) != 10 {
		t.Fatalf("GeneratedLocations = %v, want the ten closed locations", got)
	}
	locs := GeneratedLocations()
	locs[0] = "mutated"
	if GeneratedLocations()[0] != ".claude/CLAUDE.md" {
		t.Fatal("GeneratedLocations returns the shared slice")
	}
	var m Manifest
	for _, p := range generatorOutputs {
		m.Entries = append(m.Entries, Entry{Path: p})
	}
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		m.Entries = append(m.Entries, Entry{Path: r.Path})
	}
	if got := LintLocations(m); len(got) != 0 {
		t.Fatalf("LintLocations over every generator output = %+v, want none", got)
	}
	if got := LintLocations(Manifest{}); got != nil {
		t.Fatalf("LintLocations(empty) = %+v", got)
	}
}
