package generate

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wantWorkflowPaths is the closed supported output, in render order.
var wantWorkflowPaths = []string{".claude/workflows/verify.js", ".claude/workflows/review.js",
	".claude/workflows/implement.js", ".claude/workflows/cascade-workflows.json"}

// supportedProbe and its siblings are probes fixed at one result.
func supportedProbe() *stubProbe {
	return &stubProbe{r: ProbeResult{Result: resultSupported, Binary: probeBin, Version: "2.1.273"}}
}

// renderWF renders v behind p and fails on error.
func renderWF(t *testing.T, v View, p WorkflowsSupportProbe) WorkflowsLeg {
	t.Helper()
	leg, err := RenderWorkflows(t.Context(), v, p)
	if err != nil {
		t.Fatalf("RenderWorkflows: %v", err)
	}
	return leg
}

func TestWorkflowsSupported(t *testing.T) {
	root, _ := newRepo(t)
	leg := renderWF(t, templatingView(t), supportedProbe())
	if leg.Deferral != nil || len(leg.Files) != len(wantWorkflowPaths) {
		t.Fatalf("leg = %d files, deferral %+v", len(leg.Files), leg.Deferral)
	}
	for i, r := range leg.Files {
		if r.Path != wantWorkflowPaths[i] || r.GeneratorID != "workflows" {
			t.Fatalf("file %d = %s (%s)", i, r.Path, r.GeneratorID)
		}
		blocks, err := ParseManagedBlocks(r.Content, FormSlash, nil)
		if err != nil || len(blocks) != 1 || !bytes.Equal(WrapBlock(FormSlash, blocks[0].ID, blocks[0].Body), r.Content) {
			t.Fatalf("%s is not exactly one // block: %v", r.Path, err)
		}
	}
	m, outs, _ := applyAll(t, root, Manifest{}, false, leg.Files)
	for _, o := range outs {
		if !o.Written {
			t.Fatalf("outcome %+v, want written", o)
		}
	}
	if len(m.Entries) != 4 {
		t.Fatalf("manifest entries %+v", m.Entries)
	}
	for _, e := range m.Entries {
		if e.GeneratorID != "workflows" || len(e.Blocks) != 1 || !strings.HasPrefix(e.Path, ".claude/workflows/") {
			t.Fatalf("entry %+v", e)
		}
	}
	data := slurp(t, root, ".claude/workflows/cascade-workflows.json")
	for _, c := range []string{"make build", "go test ./...", "golangci-lint run"} {
		if !strings.Contains(data, strconv.Quote(c)) {
			t.Errorf("data file lacks the detected command %q", c)
		}
	}
}

func TestWorkflowsBodiesFromCompiledTemplates(t *testing.T) {
	isolate(t)
	a := renderWF(t, templatingView(t), supportedProbe()).Files
	b := renderWF(t, fixtureView(t), supportedProbe()).Files
	for i, n := range workflowNames {
		tmpl, err := fs.ReadFile(repo.TemplateFS(), "templates/workflows/"+n+".js.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a[i].Content, b[i].Content) {
			t.Fatalf("%s differs between two repositories", a[i].Path)
		}
		if !bytes.Equal(a[i].Content, WrapBlock(FormSlash, "workflow-"+n, tmpl)) {
			t.Fatalf("%s is not its template inside one block", a[i].Path)
		}
	}
	if bytes.Equal(a[3].Content, b[3].Content) {
		t.Fatal("the data file does not carry the detected commands")
	}
	for _, c := range []string{"make build", "go test ./...", "golangci-lint run"} {
		for _, r := range a[:3] {
			if bytes.Contains(r.Content, []byte(c)) {
				t.Fatalf("%s holds the repository value %q", r.Path, c)
			}
		}
	}
	for _, body := range []string{"", "x {{test}}\n", "const a = `${b}`\n", "const f = require('x')\n"} {
		if err := checkScriptBody("t.js.tmpl", []byte(body)); err == nil {
			t.Fatalf("template %q accepted", body)
		} else if ce, ok := err.(*cascade.Error); !ok || ce.Kind != cascade.KindIntegrity {
			t.Fatalf("template %q: %v, want KindIntegrity", body, err)
		}
	}
}

func TestWorkflowBodiesUseNoFsOrShell(t *testing.T) {
	isolate(t)
	bad := regexp.MustCompile(`require|import|\bfs\b|child_process|process\.|Deno\.`)
	for _, probe := range []string{"require(", "import x", "fs.read", "child_process", "process.env", "Deno.run"} {
		if !bad.MatchString(probe) || !forbiddenScriptToken.MatchString(probe) {
			t.Fatalf("token pattern misses %q", probe)
		}
	}
	for _, r := range renderWF(t, templatingView(t), supportedProbe()).Files[:3] {
		if loc := bad.Find(r.Content); loc != nil {
			t.Errorf("%s holds %q", r.Path, loc)
		}
		if !bytes.Contains(r.Content, []byte("export const meta = {")) {
			t.Errorf("%s declares no meta", r.Path)
		}
	}
}

func TestWorkflowsDeferral(t *testing.T) {
	for _, res := range []string{resultUnsupported, resultIndeterminate, resultOutOfRange} {
		t.Run(res, func(t *testing.T) {
			root, _ := newRepo(t)
			before := snapshot(t, root)
			p := &stubProbe{r: ProbeResult{Result: res, Binary: probeBin, Version: "2.1.272"}}
			leg := renderWF(t, templatingView(t), p)
			d := leg.Deferral
			if leg.Files != nil || d == nil {
				t.Fatalf("leg = %+v, want no files and a deferral", leg)
			}
			if d.Generator != "workflows" || d.Result != res || d.Owner != "DEF-P3-cc-workflows" || d.Expiry != "2026-12-21" || d.Reason == "" {
				t.Fatalf("deferral = %+v", d)
			}
			if strings.Contains(d.Reason, "rendered") && !strings.Contains(d.Reason, "no workflows rendered") {
				t.Fatalf("reason %q claims workflows exist", d.Reason)
			}
			m, _, _ := applyAll(t, root, Manifest{}, false, leg.Files)
			m.Deferrals = []Deferral{*d}
			if err := WriteManifest(root, m); err != nil {
				t.Fatal(err)
			}
			got, ok, err := ReadManifest(root)
			if err != nil || !ok || len(got.Deferrals) != 1 || got.Deferrals[0] != *d {
				t.Fatalf("manifest deferrals %+v %v", got.Deferrals, err)
			}
			for _, e := range got.Entries {
				if strings.HasPrefix(e.Path, ".claude/workflows") {
					t.Fatalf("manifest entry %s on the deferral path", e.Path)
				}
			}
			after := snapshot(t, root)
			delete(after, ".cascade")
			delete(after, filepath.FromSlash(ManifestRel))
			sameTree(t, before, after)
		})
	}
}

func TestWorkflowsRenderErrors(t *testing.T) {
	isolate(t)
	boom := cascade.New(cascade.KindUnavailable, "probe broke")
	if _, err := RenderWorkflows(t.Context(), templatingView(t), &stubProbe{err: boom}); err != boom {
		t.Fatalf("probe failure = %v, want it returned", err)
	}
	for res, want := range map[string]string{
		resultUnsupported:   "harness CLI 2.1.0 lists no workflows command; no workflows rendered",
		resultOutOfRange:    "harness CLI 2.1.0 is older than 2.1.273; no workflows rendered",
		resultIndeterminate: "harness workflows support could not be determined; no workflows rendered",
	} {
		if got := deferralReason(ProbeResult{Result: res, Binary: probeBin, Version: "2.1.0"}); got != want {
			t.Errorf("%s reason %q, want %q", res, got, want)
		}
	}
}

func TestWorkflowsScriptsNeverRequiredByFleet(t *testing.T) {
	fset := token.NewFileSet()
	var scanned int
	for _, dir := range []string{"internal/fleet", "internal/conductor", "providers/agents"} {
		err := filepath.WalkDir(filepath.Join("..", "..", "..", filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && d.Name() == "testdata":
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(p, ".go"):
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			scanned++
			for _, im := range f.Imports {
				if strings.Contains(im.Path.Value, "internal/context/generate") {
					t.Errorf("%s imports %s", p, im.Path.Value)
				}
			}
			src, err := os.ReadFile(p)
			if err == nil && bytes.Contains(src, []byte(".claude/workflows")) {
				t.Errorf("%s names .claude/workflows", p)
			}
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	if scanned == 0 {
		t.Fatal("no Go file scanned; the walk is vacuous")
	}
}

func TestWorkflowsByteStability(t *testing.T) {
	isolate(t)
	for _, p := range []func() *stubProbe{supportedProbe, func() *stubProbe {
		return &stubProbe{r: ProbeResult{Result: resultUnsupported, Binary: probeBin, Version: "2.1.316"}}
	}} {
		a, b := renderWF(t, templatingView(t), p()), renderWF(t, templatingView(t), p())
		if len(a.Files) != len(b.Files) || (a.Deferral == nil) != (b.Deferral == nil) || (a.Deferral != nil && *a.Deferral != *b.Deferral) {
			t.Fatalf("two renders differ: %+v vs %+v", a, b)
		}
		for i := range a.Files {
			if !bytes.Equal(a.Files[i].Content, b.Files[i].Content) {
				t.Fatalf("%s differs between renders", a.Files[i].Path)
			}
			if loc := timestampShape.Find(a.Files[i].Content); loc != nil {
				t.Fatalf("%s carries a timestamp-shaped value %q", a.Files[i].Path, loc)
			}
		}
	}
}

func TestWorkflowsGoldens(t *testing.T) {
	isolate(t)
	update := os.Getenv("CASCADE_UPDATE_WORKFLOWS_GOLDENS") == "1"
	for _, r := range renderWF(t, templatingView(t), supportedProbe()).Files {
		p := filepath.Join("testdata", "golden", "workflows", strings.TrimPrefix(r.Path, ".claude/workflows/"))
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

func TestWorkflowsManagedBlockRoundTrip(t *testing.T) {
	isolate(t)
	r := renderWF(t, templatingView(t), supportedProbe()).Files[0]
	head, tail := "// Hand note: run this before every push.\n", "\n// Hand footer kept by the owner.\n"
	content := append(append([]byte(head), r.Content...), tail...)
	blocks, err := ParseManagedBlocks(content, FormSlash, map[string]bool{"workflow-verify": true})
	if err != nil || len(blocks) != 1 {
		t.Fatalf("parse: %d blocks %v", len(blocks), err)
	}
	next := []byte("export const meta = { name: 'cascade-verify' }\n")
	out, err := ReplaceManagedBlock(content, FormSlash, "workflow-verify", next)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte(head)) || !bytes.HasSuffix(out, []byte(tail)) {
		t.Fatalf("hand text changed:\n%s", out)
	}
	back, err := ReplaceManagedBlock(out, FormSlash, "workflow-verify", blocks[0].Body)
	if err != nil || !bytes.Equal(back, content) {
		t.Fatalf("re-render differs (%v):\n%s", err, back)
	}
}
