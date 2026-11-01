package generate

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	cctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// applyAll applies rs through one Writer over prior and returns the next
// manifest, the outcomes and the sink recorder.
func applyAll(t *testing.T, root string, prior Manifest, hadPrior bool, rs []Rendered) (Manifest, []Outcome, *recorder) {
	t.Helper()
	rec := &recorder{}
	w, err := NewWriter(root, rec.sink(), prior, hadPrior)
	if err != nil {
		t.Fatal(err)
	}
	var outs []Outcome
	for _, r := range rs {
		out, err := w.Apply(context.Background(), r)
		if err != nil {
			t.Fatalf("Apply %s: %v", r.Path, err)
		}
		outs = append(outs, out)
	}
	return w.Manifest(fixedNow, nil), outs, rec
}

// blocksOf parses a rendered Markdown file's blocks.
func blocksOf(t *testing.T, content []byte) map[string]string {
	t.Helper()
	bs, err := ParseManagedBlocks(content, FormMarkdown, nil)
	if err != nil {
		t.Fatalf("ParseManagedBlocks: %v", err)
	}
	out := map[string]string{}
	for _, b := range bs {
		out[b.ID] = string(b.Body)
	}
	return out
}

func TestRulesCC(t *testing.T) {
	isolate(t)
	v := fixtureView(t)
	rs, err := RenderRules(v)
	if err != nil {
		t.Fatal(err)
	}
	cc := rs[0]
	if cc.Path != ".claude/CLAUDE.md" || cc.GeneratorID != "rules-cc" {
		t.Fatalf("first file = %s/%s", cc.Path, cc.GeneratorID)
	}
	mc := ccMerged(v)
	if len(mc.Sections) != 2 || mc.Sections[0].Heading != "## Repository invariants" || mc.Sections[1].Heading != "## Cascade availability" {
		t.Fatalf("CLAUDE.md MergedContext = %+v, want invariants and availability only", mc.Sections)
	}
	files, err := (&cctx.CCInstructionWriter{}).Generate(mc)
	if err != nil || len(files) != 1 {
		t.Fatalf("CCInstructionWriter: %v, %d files", err, len(files))
	}
	blocks := blocksOf(t, cc.Content)
	if len(blocks) != 2 || blocks["cc-invariants"] != string(files[0].Content) {
		t.Fatalf("cc-invariants block is not the CC writer's output:\n%s", cc.Content)
	}
	spans, _ := scanBlocks(cc.Content, FormMarkdown, nil)
	if rest := outsideBytes(cc.Content, spans); len(rest) != 0 {
		t.Fatalf("CLAUDE.md has bytes outside its blocks: %q", rest)
	}
	for _, perPath := range []string{"go build", "go test ./...", "golangci-lint", "Path rules", "layout", "AGENTS.md` at"} {
		if strings.Contains(string(cc.Content), perPath) {
			t.Errorf("CLAUDE.md carries per-path detail %q", perPath)
		}
	}
	if !strings.Contains(string(cc.Content), "Continuous integration: `github-actions`.") {
		t.Fatalf("CLAUDE.md lacks the CI invariant:\n%s", cc.Content)
	}
}

func TestRulesCCFallbackLine(t *testing.T) {
	isolate(t)
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	const line = "if the Cascade MCP server is unavailable, use the `cascade` CLI for the same operations"
	if !strings.Contains(string(rs[0].Content), "\n- "+line+"\n") {
		t.Fatalf("CLAUDE.md lacks the R-16.43 line verbatim:\n%s", rs[0].Content)
	}
}

func TestCIListAndInlineCode(t *testing.T) {
	if got := ciList(fixtureView(t).CI); got != "`github-actions`" {
		t.Fatalf("ciList = %q", got)
	}
	v := fixtureView(t)
	v.CI.GitHubActions, v.CI.GitLabCI, v.CI.CircleCI = false, true, true
	if got := ciList(v.CI); got != "`gitlab-ci`, `circleci`" {
		t.Fatalf("ciList = %q", got)
	}
	v.CI.GitLabCI, v.CI.CircleCI = false, false
	if got := ciList(v.CI); got != "`none`" {
		t.Fatalf("ciList = %q", got)
	}
	cases := map[string]string{
		"":                   "` `",
		"plain":              "`plain`",
		"a`b":                "``a`b``",
		"`x``":               "``` `x`` ```",
		"line\n<!-- x -->\r": "`line <!-- x --> `",
		"   ":                "`     `",
	}
	for in, want := range cases {
		if got := inlineCode(in); got != want {
			t.Errorf("inlineCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeneratePackageHasNoCLISurface(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(paths))
	}
	fset := token.NewFileSet()
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "cobra") || strings.Contains(path, "/internal/rpc") ||
				strings.HasPrefix(path, "github.com/acamarata/cascade/cmd") {
				t.Errorf("%s imports %s: the generate package registers no CLI verb or RPC method", p, path)
			}
		}
	}
}

func TestKernelBlockGenerated(t *testing.T) {
	isolate(t)
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Path != ".claude/CLAUDE.md" && r.Path != provider.PolicyKernelFile {
			continue
		}
		if got := blocksOf(t, r.Content)["policy-kernel@1"]; got != policy.KernelText {
			t.Fatalf("%s policy-kernel@1 block = %q, want KernelText", r.Path, got)
		}
	}
	last := rs[len(rs)-1]
	if last.Path != ".cascade/policy-kernel.md" || last.GeneratorID != "policy-kernel" ||
		string(last.Content) != string(WrapBlock(FormMarkdown, "policy-kernel@1", []byte(policy.KernelText))) ||
		string(last.Content) != string(policy.RenderKernelFile()) {
		t.Fatalf("kernel file = %s/%s %q", last.Path, last.GeneratorID, last.Content)
	}
}

func TestKernelStandaloneFileWritten(t *testing.T) {
	root, _ := newRepo(t)
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	m, outs, rec := applyAll(t, root, Manifest{}, false, rs)
	for _, o := range outs {
		if !o.Written {
			t.Fatalf("outcome %+v, want written", o)
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("sink calls %+v", rec.calls)
	}
	if err := WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadManifest(root)
	if err != nil || !ok {
		t.Fatalf("ReadManifest: %v %v", ok, err)
	}
	var found bool
	for _, e := range got.Entries {
		if e.Path == provider.PolicyKernelFile {
			found = e.GeneratorID == "policy-kernel" && len(e.Blocks) == 1 && e.Blocks[0].ID == "policy-kernel@1"
		}
	}
	if !found {
		t.Fatalf("manifest has no policy-kernel entry: %+v", got.Entries)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(provider.PolicyKernelFile)))
	if err != nil || string(data) != string(policy.RenderKernelFile()) {
		t.Fatalf("kernel on disk = %q, %v", data, err)
	}
	if created, err := policy.EnsureKernelFile(root); created || err != nil {
		t.Fatalf("EnsureKernelFile over the generated kernel = %v, %v; want a no-op", created, err)
	}
	if d, err := Check(context.Background(), root); err != nil || !d.Clean() {
		t.Fatalf("Check = %+v, %v", d, err)
	}
}

func TestRenderRulesRefusals(t *testing.T) {
	isolate(t)
	_, err := RenderRules(View{})
	wantKind(t, err, cascade.KindInvalidInput, "generate: policy view has no scope")
	v := fixtureView(t)
	v.Scopes = append(v.Scopes, v.Scopes[0])
	_, err = RenderRules(v)
	wantKind(t, err, cascade.KindInvalidInput, `generate: two scopes share slug "root"`)
	v.Scopes = []ScopeView{{Slug: "web", Rel: "apps/web"}}
	_, err = RenderRules(v)
	wantKind(t, err, cascade.KindInvalidInput, `generate: scope "apps/web" has slug "web", want "apps-web"`)
	long := strings.Repeat("a", 70)
	v.Scopes = []ScopeView{{Slug: long, Rel: long}}
	_, err = RenderRules(v)
	wantKind(t, err, cascade.KindInvalidInput, `generate: scope slug "`+long+`" makes no valid block id`)
}
