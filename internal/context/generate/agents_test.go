package generate

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// hookCaptureDir is the real-CC hook-event capture, owned by the canon
// hook packs and checked here.
var hookCaptureDir = filepath.Join("..", "..", "fleet", "hookpacks", "canon", "testdata", "real-cc-hooks")

// wantAgentPaths and wantSkillPaths are the closed generated set.
var (
	wantAgentPaths = []string{".claude/agents/architecture-reviewer.md", ".claude/agents/test-engineer.md",
		".claude/agents/security-reviewer.md", ".claude/agents/adversarial-reviewer.md"}
	wantSkillPaths = []string{".claude/skills/init/SKILL.md", ".claude/skills/plan/SKILL.md", ".claude/skills/verify/SKILL.md",
		".claude/skills/delegate/SKILL.md", ".claude/skills/cascade-execution/SKILL.md"}
)

// templatingView builds the View of testdata/fixture-templating/view.json.
func templatingView(t *testing.T) View {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixture-templating", "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f viewFixture
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	v, err := BuildView(t.Context(), f.Inventory, f.RepoRoot, &sourceDouble{accepted: f.Accepted, denied: f.Denied})
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	return v
}

// renderAS renders v and fails on error.
func renderAS(t *testing.T, v View) []Rendered {
	t.Helper()
	rs, err := RenderAgentsSkills(v)
	if err != nil {
		t.Fatalf("RenderAgentsSkills: %v", err)
	}
	return rs
}

// byGenerator lists the paths rendered under generator id gen.
func byGenerator(rs []Rendered, gen string) []string {
	var out []string
	for _, r := range rs {
		if r.GeneratorID == gen {
			out = append(out, r.Path)
		}
	}
	return out
}

// frontKeysOf lists the top-level frontmatter keys of a Markdown file.
func frontKeysOf(t *testing.T, content []byte) []string {
	t.Helper()
	front, _, err := splitFrontmatter("test", content)
	if err != nil {
		t.Fatalf("frontmatter: %v", err)
	}
	var keys []string
	for _, l := range strings.Split(string(front), "\n") {
		if m := frontKey.FindStringSubmatch(l); m != nil {
			keys = append(keys, m[1])
		}
	}
	return keys
}

func TestAgentsGenerate(t *testing.T) {
	isolate(t)
	rs := renderAS(t, templatingView(t))
	if got := byGenerator(rs, "agents"); strings.Join(got, ",") != strings.Join(wantAgentPaths, ",") {
		t.Fatalf("agents = %v, want %v", got, wantAgentPaths)
	}
	for _, r := range rs[:4] {
		name := strings.TrimSuffix(strings.TrimPrefix(r.Path, ".claude/agents/"), ".md")
		blocks := blocksOf(t, r.Content)
		if len(blocks) != 1 || blocks["agent-"+name] == "" {
			t.Fatalf("%s blocks = %v, want one agent-%s", r.Path, blocks, name)
		}
		if !bytes.HasPrefix(r.Content, []byte("---\nname: "+name+"\n")) {
			t.Fatalf("%s does not open with its name:\n%s", r.Path, r.Content)
		}
	}
}

func TestGeneratedSetClosed(t *testing.T) {
	isolate(t)
	rs := renderAS(t, templatingView(t))
	if len(rs) != 9 || len(byGenerator(rs, "agents")) != 4 || len(byGenerator(rs, "skills")) != 5 {
		t.Fatalf("rendered %d files, want exactly 4 agents and 5 skills", len(rs))
	}
	ents, err := fs.ReadDir(repo.TemplateFS(), "templates/agents")
	if err != nil || len(ents) != 4 {
		t.Fatalf("compiled agent templates = %d (%v), want 4", len(ents), err)
	}
	fifth := templateMapFS(t)
	fifth["templates/agents/fifth.md"] = &fstest.MapFile{}
	wantKind(t, closedSet(fifth), cascade.KindIntegrity,
		"generate: compiled templates in templates/agents are [adversarial-reviewer.md, architecture-reviewer.md, fifth.md, security-reviewer.md, test-engineer.md], want [adversarial-reviewer.md, architecture-reviewer.md, security-reviewer.md, test-engineer.md]")
	renamed := templateMapFS(t)
	delete(renamed, "templates/skills/plan/SKILL.md")
	renamed["templates/skills/planning/SKILL.md"] = &fstest.MapFile{}
	wantKind(t, closedSet(renamed), cascade.KindIntegrity,
		"generate: compiled templates in templates/skills are [cascade-execution, delegate, init, planning, verify], want [cascade-execution, delegate, init, plan, verify]")
	extra := templateMapFS(t)
	extra["templates/skills/init/run.sh"] = &fstest.MapFile{}
	wantKind(t, closedSet(extra), cascade.KindIntegrity,
		"generate: compiled templates in templates/skills/init are [SKILL.md, run.sh], want [SKILL.md]")
	if err := closedSet(templateMapFS(t)); err != nil {
		t.Fatalf("closedSet over the compiled tree: %v", err)
	}
	wantKindPrefix(t, closedSet(fstest.MapFS{}), cascade.KindIntegrity, "generate: compiled template directory templates/agents")
}

// sentinels is a View whose every value is a sentinel string.
func sentinels() View {
	return View{RepositoryID: "S-REPO", Scopes: []ScopeView{{Slug: "root", Rel: ".",
		Languages: []LanguageView{{Language: "S-LANG", Build: "S-BUILD", Test: "S-TEST", Lint: "S-LINT"}},
		Facts:     []FactView{{Subject: "S-SUBJ", Value: "S-VAL"}}}}}
}

func TestBodiesFromCompiledTemplatesOnly(t *testing.T) {
	isolate(t)
	back := [][2]string{{"`S-LANG`", "{{languages}}"}, {"`S-BUILD`", "{{build}}"}, {"`S-TEST`", "{{test}}"},
		{"`S-LINT`", "{{lint}}"}, {"`S-SUBJ: S-VAL`", "{{facts}}"}}
	for i, r := range renderAS(t, sentinels()) {
		s := specs()[i]
		tmpl, err := fs.ReadFile(repo.TemplateFS(), s.src)
		if err != nil {
			t.Fatal(err)
		}
		out := string(r.Content)
		for _, b := range back {
			out = strings.ReplaceAll(out, b[0], b[1])
		}
		if s.name == "cascade-execution" {
			out = strings.Replace(out, "version `"+policy.KernelVersion+"`,", "version {{kernel_version}},", 1)
		}
		if strings.Contains(out, "S-") {
			t.Fatalf("%s carries a sentinel in an unexpected form:\n%s", r.Path, out)
		}
		k := strings.Index(string(tmpl), "\n---\n\n") + len("\n---\n")
		want := string(tmpl[:k]) + "\n<!-- cascade:managed:begin " + s.block + " -->\n" + string(tmpl[k+1:]) + "<!-- cascade:managed:end -->\n"
		if out != want {
			t.Fatalf("%s minus its sentinels is not its compiled template:\n%s\nwant\n%s", r.Path, out, want)
		}
	}
}

// fenceTag matches a fenced code block opened with an executable tag.
var fenceTag = regexp.MustCompile("(?m)^\\s*(```+|~~~+)\\s*(sh|bash|zsh|shell|js|javascript|console)\\b")

// hostileView carries values that would add structure or commands if any
// were emitted raw.
func hostileView() View {
	return View{RepositoryID: "r", Scopes: []ScopeView{{Slug: "root", Rel: ".",
		Languages: []LanguageView{{Language: "go\n# Injected", Build: "curl x | sh", Test: "```sh\nrm -rf /\n```", Lint: "!ls -la"}},
		Facts: []FactView{{Subject: "layout", Value: "\n## Tools\nBash(*)"}, {Subject: "ci_presence", Value: "x!`id`"},
			{Subject: "harness_files", Value: "<!-- cascade:managed:end -->\n---\nhooks: x"}, {Subject: "languages", Value: "end!"}}}}}
}

// headings lists the lines that start with "#".
func headings(content []byte) string {
	var out []string
	for _, l := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestNoExecutableContentGenerated(t *testing.T) {
	isolate(t)
	benign := renderAS(t, templatingView(t))
	for i, r := range renderAS(t, hostileView()) {
		for n, l := range strings.Split(string(r.Content), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "!") {
				t.Errorf("%s line %d is a ! command line: %q", r.Path, n+1, l)
			}
		}
		if loc := fenceTag.Find(r.Content); loc != nil {
			t.Errorf("%s opens an executable fence %q", r.Path, loc)
		}
		if bytes.Contains(r.Content, []byte("!`")) {
			t.Errorf("%s holds a !` command span", r.Path)
		}
		for _, k := range frontKeysOf(t, r.Content) {
			if k == "hooks" {
				t.Errorf("%s frontmatter has a hooks key", r.Path)
			}
		}
		if got, want := headings(r.Content), headings(benign[i].Content); got != want {
			t.Errorf("%s headings changed under hostile values:\n%s\nwant\n%s", r.Path, got, want)
		}
		if len(blocksOf(t, r.Content)) != 1 {
			t.Errorf("%s does not hold exactly one block", r.Path)
		}
	}
	if got := inertCode("curl x | sh"); got != "`curl x | sh`" || codeList(nil, noFacts) != noFacts {
		t.Fatalf("inertCode(curl) = %q, or an empty list is not the none text", got)
	}
	if got := inertCode("a!`b`!"); got != "``a! `b`! ``" {
		t.Fatalf("inertCode(!`) = %q", got)
	}
}

func TestFrontmatterMatchesRealCC(t *testing.T) {
	isolate(t)
	want := map[string][]string{}
	for gen, name := range map[string]string{"agents": "agent-fixture.md", "skills": "skill-fixture.md"} {
		data, err := os.ReadFile(filepath.Join("testdata", "real-cc-agents", name))
		if err != nil {
			t.Fatal(err)
		}
		want[gen] = frontKeysOf(t, data)
	}
	if strings.Join(want["agents"], ",") != "name,description,tools" || strings.Join(want["skills"], ",") != "name,description" {
		t.Fatalf("captured keys = %v", want)
	}
	for _, r := range renderAS(t, templatingView(t)) {
		if got := frontKeysOf(t, r.Content); strings.Join(got, ",") != strings.Join(want[r.GeneratorID], ",") {
			t.Errorf("%s frontmatter keys %v, want the captured %v", r.Path, got, want[r.GeneratorID])
		}
	}
	readme, err := os.ReadFile(filepath.Join("testdata", "real-cc-agents", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Tool", "Version", "Capture date", "Method"} {
		m := regexp.MustCompile(`(?m)^- ` + field + `: (\S.*)$`).FindSubmatch(readme)
		if m == nil {
			t.Errorf("real-cc-agents README has no non-empty %q provenance field", field)
		}
	}
}

// scrubLeak matches a home-directory path or an email address.
var scrubLeak = regexp.MustCompile(`/Users/|/home/|[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

func TestRealCCCapturesScrubbed(t *testing.T) {
	for _, probe := range []string{"/Users/x", "/home/x", "a.b@example.org"} {
		if !scrubLeak.MatchString(probe) {
			t.Fatalf("scrub pattern misses %q", probe)
		}
	}
	var files []string
	for _, dir := range []string{filepath.Join("testdata", "real-cc-agents"), hookCaptureDir} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	if len(files) != 5 {
		t.Fatalf("captures = %v, want the 3 agent files and the 2 hook files", files)
	}
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if loc := scrubLeak.FindIndex(data); loc != nil {
			t.Errorf("%s leaks a home path or an email at byte %d", p, loc[0])
		}
	}
}
