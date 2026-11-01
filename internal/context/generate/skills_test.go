package generate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// templateMapFS copies the compiled template tree into a MapFS a test may
// change.
func templateMapFS(t *testing.T) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	for p := range repo.TemplateHashes() {
		data, err := fs.ReadFile(repo.TemplateFS(), p)
		if err != nil {
			t.Fatal(err)
		}
		m[p] = &fstest.MapFile{Data: data}
	}
	return m
}

// wantKindPrefix asserts err is a cascade error of kind whose message
// starts with prefix.
func wantKindPrefix(t *testing.T, err error, kind cascade.Kind, prefix string) {
	t.Helper()
	ce, ok := err.(*cascade.Error)
	if !ok || ce.Kind != kind || !strings.HasPrefix(ce.Msg, prefix) {
		t.Fatalf("error = %v, want %s starting %q", err, kind, prefix)
	}
}

// file finds the rendered file at path.
func file(t *testing.T, rs []Rendered, path string) string {
	t.Helper()
	for _, r := range rs {
		if r.Path == path {
			return string(r.Content)
		}
	}
	t.Fatalf("no rendered %s", path)
	return ""
}

func TestSkillsGenerate(t *testing.T) {
	isolate(t)
	rs := renderAS(t, templatingView(t))
	if got := byGenerator(rs, "skills"); strings.Join(got, ",") != strings.Join(wantSkillPaths, ",") {
		t.Fatalf("skills = %v, want %v", got, wantSkillPaths)
	}
	for _, r := range rs[4:] {
		name := strings.TrimSuffix(strings.TrimPrefix(r.Path, ".claude/skills/"), "/SKILL.md")
		if blocks := blocksOf(t, r.Content); len(blocks) != 1 || blocks["skill-"+name] == "" {
			t.Fatalf("%s blocks = %v, want one skill-%s", r.Path, blocks, name)
		}
	}
}

func TestExecutionSkillGenerated(t *testing.T) {
	isolate(t)
	got := file(t, renderAS(t, templatingView(t)), ".claude/skills/cascade-execution/SKILL.md")
	if !strings.Contains(got, "policy kernel, version `"+policy.KernelVersion+"`, in `.cascade/policy-kernel.md`") {
		t.Fatalf("cascade-execution does not name KernelVersion:\n%s", got)
	}
}

func TestExecutionSkillListsPushbackAndInterpretations(t *testing.T) {
	isolate(t)
	got := blocksOf(t, []byte(file(t, renderAS(t, templatingView(t)), ".claude/skills/cascade-execution/SKILL.md")))["skill-cascade-execution"]
	for _, rule := range []string{"\n4. PUSHBACK: when the task", "\n5. INTERPRETATIONS: when the task can be read"} {
		if !strings.Contains(got, rule) {
			t.Errorf("cascade-execution block lacks %q:\n%s", rule, got)
		}
	}
}

func TestTemplatingCorrectness(t *testing.T) {
	isolate(t)
	rs := renderAS(t, templatingView(t))
	for i, r := range rs {
		tmpl, _ := fs.ReadFile(repo.TemplateFS(), specs()[i].src)
		for ph, want := range map[string]string{"{{build}}": "`make build`", "{{test}}": "`go test ./...`", "{{lint}}": "`golangci-lint run`"} {
			if bytes.Contains(tmpl, []byte(ph)) && !bytes.Contains(r.Content, []byte(want)) {
				t.Errorf("%s lacks %s", r.Path, want)
			}
		}
		for _, bad := range []string{"{{", "}}", noneDetected, "never-rendered"} {
			if bytes.Contains(r.Content, []byte(bad)) {
				t.Errorf("%s carries %q", r.Path, bad)
			}
		}
	}
	if !strings.Contains(file(t, rs, ".claude/agents/architecture-reviewer.md"), "Accepted facts: `` layout: commands live in `cmd/` ``") {
		t.Fatal("the accepted fact is not rendered as one code span")
	}
}

func TestAgentsSkillsPreserveHandEdits(t *testing.T) {
	root, _ := newRepo(t)
	rs := renderAS(t, templatingView(t))
	w, _ := NewWriter(root, (&recorder{}).sink(), Manifest{}, false)
	outs, err := WriteAgentsSkills(t.Context(), w, rs)
	if err != nil || len(outs) != 9 {
		t.Fatalf("first write: %d outcomes, %v", len(outs), err)
	}
	m := w.Manifest(fixedNow, nil)
	for i, e := range m.Entries {
		if len(e.Blocks) != 1 || e.Blocks[0].GeneratorID != e.GeneratorID || !strings.HasPrefix(e.Blocks[0].ID, strings.TrimSuffix(e.GeneratorID, "s")+"-") {
			t.Fatalf("entry %d = %+v, want one agent-/skill- block", i, e)
		}
	}
	target := ".claude/agents/test-engineer.md"
	edited := slurp(t, root, target) + "\nHand note: run the slow suite on Fridays.\n"
	put(t, root, target, edited)
	rec := &recorder{}
	w2, _ := NewWriter(root, rec.sink(), m, true)
	outs, err = WriteAgentsSkills(t.Context(), w2, renderAS(t, templatingView(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		switch {
		case o.Path == target && (o.Reason != ReasonOutsideEdited || o.Sidecar != target+".cascade-new"):
			t.Fatalf("hand-edited outcome %+v", o)
		case o.Path != target && !o.Adopted:
			t.Fatalf("untouched outcome %+v, want adopted", o)
		}
	}
	if slurp(t, root, target) != edited || len(rec.calls) != 1 {
		t.Fatalf("hand addition lost or sink calls %+v", rec.calls)
	}
	if _, err := WriteAgentsSkills(t.Context(), nil, rs); err == nil {
		t.Fatal("nil Writer accepted")
	}
}

func TestAgentsSkillsByteStable(t *testing.T) {
	isolate(t)
	a, b := renderAS(t, templatingView(t)), renderAS(t, templatingView(t))
	for i := range a {
		if a[i].Path != b[i].Path || !bytes.Equal(a[i].Content, b[i].Content) {
			t.Fatalf("render %d differs: %s", i, a[i].Path)
		}
		if loc := timestampShape.Find(a[i].Content); loc != nil {
			t.Fatalf("%s carries a timestamp-shaped value %q", a[i].Path, loc)
		}
	}
}

func TestAgentsSkillsGoldens(t *testing.T) {
	isolate(t)
	update := os.Getenv("CASCADE_UPDATE_AGENTS_SKILLS_GOLDENS") == "1"
	for _, r := range renderAS(t, templatingView(t)) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.Path, ".claude/skills/"), "/SKILL.md") + ".md"
		p := filepath.Join("testdata", "golden", "skills", name)
		if r.GeneratorID == "agents" {
			p = filepath.Join("testdata", "golden", "agents", strings.TrimPrefix(r.Path, ".claude/agents/"))
		}
		if update {
			if err := os.WriteFile(p, r.Content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(r.Content, want) {
			t.Errorf("%s differs from %s (%v):\n%s", r.Path, p, err, r.Content)
		}
	}
}

func TestAgentsSkillsErrorPaths(t *testing.T) {
	rs := renderAS(t, templatingView(t))
	t.Run("no-commands", func(t *testing.T) {
		v := View{Scopes: []ScopeView{{Slug: "root", Rel: ".", Languages: []LanguageView{{Language: "go"}}}}}
		got, err := RenderAgentsSkills(v)
		wantKind(t, err, cascade.KindInvalidInput, "generate: the inventory names no build, test or lint command")
		if got != nil {
			t.Fatalf("rendered %d files with no commands", len(got))
		}
	})
	t.Run("mangled-marker", func(t *testing.T) {
		root, _ := newRepo(t)
		put(t, root, ".claude/skills/cascade-execution/SKILL.md", "<!-- cascade:managed:begin skill-cascade-execution -->\nx\n")
		err := writeRefused(t, root, rs)
		wantKind(t, err, cascade.KindInvalidInput, "generate: .claude/skills/cascade-execution/SKILL.md holds a mangled managed-block marker; nothing was written")
		inner := errors.Unwrap(errors.Unwrap(err))
		if inner != error(ErrMangledMarker) || !isMangled(err) {
			t.Fatalf("cause = %v, want ErrMangledMarker itself", inner)
		}
	})
	t.Run("unwritable-agents-dir", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("directory permissions do not bind here")
		}
		root, _ := newRepo(t)
		dir := filepath.Join(root, ".claude", "agents")
		if err := errors.Join(os.MkdirAll(dir, 0o755), os.Chmod(dir, 0o555)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		wantKindPrefix(t, writeRefused(t, root, rs), cascade.KindPermissionDenied, "generate: .claude/agents is not writable; nothing was written")
	})
	t.Run("unreadable-target", func(t *testing.T) {
		root, _ := newRepo(t)
		put(t, root, ".claude/agents/test-engineer.md/inner", "x")
		wantKindPrefix(t, writeRefused(t, root, rs), cascade.KindUnavailable, "generate: read .claude/agents/test-engineer.md")
	})
}

// writeRefused runs WriteAgentsSkills over root, which must refuse with no
// write and no sink call, and returns the error.
func writeRefused(t *testing.T, root string, rs []Rendered) error {
	t.Helper()
	before := snapshot(t, root)
	rec := &recorder{}
	w, _ := NewWriter(root, rec.sink(), Manifest{}, false)
	outs, err := WriteAgentsSkills(t.Context(), w, rs)
	if err == nil || outs != nil || len(rec.calls) != 0 {
		t.Fatalf("WriteAgentsSkills = %v, %v, sink %v; want a refusal", outs, err, rec.calls)
	}
	sameTree(t, before, snapshot(t, root))
	return err
}

func TestRenderTemplateRefusals(t *testing.T) {
	vals, _ := templateValues(templatingView(t))
	s := specs()[0]
	head := "---\nname: architecture-reviewer\ndescription: d\ntools:\n  - Read\n"
	for _, tmpl := range []string{"# x\n", head + "---\n\n", strings.Replace(head, ": d", ": {{build}}", 1) + "---\n\nx\n",
		head + "hooks: x\n---\n\nx\n", strings.Replace(head, "architecture-reviewer", "other", 1) + "---\n\nx\n",
		head + "---\n\n{{secret}}\n", head + "---\n\n{{build\n"} {
		_, err := renderTemplate(fstest.MapFS{s.src: {Data: []byte(tmpl)}}, s, vals)
		wantKindPrefix(t, err, cascade.KindIntegrity, "generate: compiled template "+s.src)
	}
	_, err := renderTemplate(fstest.MapFS{}, s, vals)
	wantKindPrefix(t, err, cascade.KindIntegrity, "generate: read compiled template")
	_, err = renderTemplate(fstest.MapFS{s.src: {Data: []byte(head + "---\n\n<!-- cascade:managed:end -->\n")}}, s, vals)
	if !isMangled(err) {
		t.Fatalf("marker body = %v, want ErrMangledMarker", err)
	}
}

func TestHookCaptureShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(hookCaptureDir, "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var evs []struct {
		Event, Summary, Description string
		Exit2Line                   string `json:"exit2_line"`
		OtherExitLine               string `json:"other_exit_line"`
	}
	if err := dec.Decode(&evs); err != nil {
		t.Fatal(err)
	}
	seen, exit2 := map[string]bool{}, 0
	name := regexp.MustCompile(`^[A-Z][A-Za-z]+$`)
	for _, e := range evs {
		if !name.MatchString(e.Event) || seen[e.Event] || e.Event == "WithheldNotice" || e.Summary == "" {
			t.Errorf("bad event %+v", e)
		}
		seen[e.Event] = true
		if e.Exit2Line != "" {
			exit2++
			if !strings.Contains(e.Description, "Exit code 2 - "+e.Exit2Line+"\n") && !strings.HasSuffix(e.Description, "Exit code 2 - "+e.Exit2Line) {
				t.Errorf("%s exit2_line is not its description's line", e.Event)
			}
		}
	}
	if len(evs) != 33 || exit2 != 19 {
		t.Fatalf("capture holds %d events and %d exit2 lines, want 33 and 19", len(evs), exit2)
	}
	if wt := evs[27]; wt.Event != "WorktreeCreate" || wt.Exit2Line != "" || wt.OtherExitLine != "worktree creation failed" {
		t.Fatalf("WorktreeCreate rule = %+v, want its Other exit codes line", wt)
	}
	readme, err := os.ReadFile(filepath.Join(hookCaptureDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, re := range []string{`Binary sha256: ` + "`" + `([0-9a-f]{8} ){7}[0-9a-f]{8}` + "`", `Version: \d+\.\d+\.\d+`, `Capture date: \d{4}-\d{2}-\d{2}`, `## Extraction`, `## Scrub rules`} {
		if !regexp.MustCompile(re).Match(readme) {
			t.Errorf("hook capture README lacks %s", re)
		}
	}
}
