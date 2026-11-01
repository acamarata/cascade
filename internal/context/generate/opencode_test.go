package generate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stripSlashComments drops the whole-line // comments of a JSONC file.
func stripSlashComments(content []byte) []byte {
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(content), "\n") {
		if !strings.HasPrefix(strings.TrimLeft(line, " \t"), "//") {
			b.WriteString(line)
		}
	}
	return []byte(b.String())
}

// ownedOpenCode is the whole opencode.json the leg may write, as data.
var ownedOpenCode = map[string]any{"mcp": map[string]any{"cascade": map[string]any{
	"type": "local", "command": []any{"cascade", "mcp", "serve", "--stdio"}, "enabled": true,
}}}

func TestOpenCodeScopedGenerate(t *testing.T) {
	repo, _ := newRepo(t)
	rs, by := renderHarness(t)
	v := fixtureView(t)
	mc := scopedMerged(v, v.Scopes[0])
	cx, err := (&cctx.CXInstructionWriter{}).Generate(mc)
	if err != nil {
		t.Fatal(err)
	}
	oc, err := (&cctx.OCInstructionWriter{}).Generate(mc)
	if err != nil || len(oc) != 1 || string(oc[0].Content) != string(cx[0].Content) {
		t.Fatalf("OC writer output differs from CX: %v", err)
	}
	if got := blocksOf(t, by["AGENTS.md"].Content); got["agents-md-root"] != string(oc[0].Content) {
		t.Fatal("AGENTS.md block is not the OC writer's bytes")
	}
	cfg := by["opencode.json"]
	if cfg.GeneratorID != "opencode-config" {
		t.Fatalf("generator id = %q", cfg.GeneratorID)
	}
	const member = "    \"cascade\": {\"type\": \"local\", \"command\": [\"cascade\", \"mcp\", \"serve\", \"--stdio\"], \"enabled\": true}\n"
	if got := blockBodies(t, cfg.Content, FormSlash); len(got) != 1 || got[OpenCodeConfigBlock] != member {
		t.Fatalf("opencode-config block = %q", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(stripSlashComments(cfg.Content), &doc); err != nil {
		t.Fatalf("opencode.json is not JSONC: %v\n%s", err, cfg.Content)
	}
	if !reflect.DeepEqual(doc, ownedOpenCode) {
		t.Fatalf("opencode.json keys = %v, want only the owned member", doc)
	}
	m, _, _ := applyAll(t, repo, Manifest{}, false, rs)
	found := false
	for _, e := range m.Entries {
		if e.Path == "opencode.json" {
			found = e.GeneratorID == "opencode-config" && len(e.Blocks) == 1 && e.Blocks[0].ID == OpenCodeConfigBlock
		}
	}
	if !found || slurp(t, repo, "opencode.json") != string(cfg.Content) {
		t.Fatalf("opencode.json not recorded by the Writer: %+v", m.Entries)
	}
}

func TestOpenCodeRealParserAcceptsConfig(t *testing.T) {
	c := loadCapture(t, "real-opencode", "opencode.json", "opencode")
	if c.Command != "opencode debug config" {
		t.Fatalf("command = %q", c.Command)
	}
	var resolved struct {
		MCP map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(c.StdoutExcerpt), &resolved); err != nil {
		t.Fatalf("stdout_excerpt is not the resolved config: %v", err)
	}
	if !reflect.DeepEqual(resolved.MCP, ownedOpenCode["mcp"]) {
		t.Fatalf("opencode resolved mcp = %v, want the owned member only", resolved.MCP)
	}
}

func TestScopedHarnessPreservesHandEdits(t *testing.T) {
	repo, _ := newRepo(t)
	rs, by := renderHarness(t)
	m, _, _ := applyAll(t, repo, Manifest{}, false, rs)
	agents := "# Notes kept by hand\n\n" + string(by["AGENTS.md"].Content)
	oc := strings.Replace(string(by["opencode.json"].Content), "{\n", "{\n  \"model\": \"hand/picked\",\n", 1)
	put(t, repo, "AGENTS.md", agents)
	put(t, repo, "opencode.json", oc)
	_, outs, rec := applyAll(t, repo, m, true, rs)
	for _, o := range outs {
		switch o.Path {
		case "AGENTS.md", "opencode.json":
			if o.Written || o.Reason != ReasonOutsideEdited || missing(repo, o.Sidecar) {
				t.Fatalf("%s outcome = %+v, want an outside-edited refusal with a sidecar", o.Path, o)
			}
		default:
			if !o.Adopted {
				t.Fatalf("%s outcome = %+v, want adopted", o.Path, o)
			}
		}
	}
	if slurp(t, repo, "AGENTS.md") != agents || slurp(t, repo, "opencode.json") != oc {
		t.Fatal("a hand addition did not survive regeneration")
	}
	if len(rec.calls) != 2 {
		t.Fatalf("sink calls = %+v, want two", rec.calls)
	}
}

func TestScopedVsUnscopedOnlyNarrows(t *testing.T) {
	isolate(t)
	v := fixtureView(t)
	scoped := scopedMerged(v, v.Scopes[0])
	parts := make([]string, len(scoped.Sections))
	for i, s := range scoped.Sections {
		parts[i] = s.Content
	}
	unscoped, err := cctx.MergeTiers([]cctx.TierRecord{
		{Role: cctx.TierGCI, Ordinal: 0, Content: "## Owner rules\n\nShort sentences.\n"},
		{Role: cctx.TierPRC, Ordinal: 3, Content: strings.Join(parts, "\n\n") + "\n"},
	})
	if err != nil {
		t.Fatalf("MergeTiers: %v", err)
	}
	_, by := renderHarness(t)
	body := blocksOf(t, by["AGENTS.md"].Content)["agents-md-root"]
	for _, w := range []cctx.HarnessGenerator{&cctx.CXInstructionWriter{}, &cctx.OCInstructionWriter{}} {
		files, err := w.Generate(unscoped)
		if err != nil || len(files) != 2 {
			t.Fatalf("unscoped Generate: %d files, %v", len(files), err)
		}
		for _, f := range files {
			switch f.Role {
			case cctx.TierPRC:
				if f.Name != "AGENTS.md" || string(f.Content) != body {
					t.Fatalf("scoped AGENTS.md differs from the unscoped PRC file:\n%s\n---\n%s", body, f.Content)
				}
			case cctx.TierGCI:
				if strings.Contains(body, "Owner rules") {
					t.Fatal("scoped output carries the omitted GCI tier")
				}
			case cctx.TierAPC, cctx.TierPPC, cctx.TierPAC:
				fallthrough
			default:
				t.Fatalf("unexpected tier %v", f.Role)
			}
		}
	}
}

func TestScopedHarnessKeepsProjectorBlock(t *testing.T) {
	repo, _ := newRepo(t)
	rs, by := renderHarness(t)
	m, _, _ := applyAll(t, repo, Manifest{}, false, rs)
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), m, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const projKeys = "[mcp_servers.other]\ncommand = \"other\"\n"
	if out, err := w.ApplyBlock(ctx, ".codex/config.toml", "projector-x", "projector-x", []byte(projKeys)); err != nil || !out.Written {
		t.Fatalf("ApplyBlock config.toml: %+v %v", out, err)
	}
	_, err = w.ApplyBlock(ctx, "opencode.json", "projector-x", "projector-x", []byte("\"other\": {}\n"))
	wantKind(t, err, cascade.KindUnsupported, `generate: cannot append block "projector-x" to opencode.json: no in-object anchor is defined`)
	if slurp(t, repo, "opencode.json") != string(by["opencode.json"].Content) {
		t.Fatal("a refused projector append changed opencode.json")
	}
	m2 := w.Manifest(fixedNow, nil)
	m3, outs, rec2 := applyAll(t, repo, m2, true, rs)
	for _, o := range outs {
		if o.Reason != "" || (!o.Written && !o.Adopted) {
			t.Fatalf("re-render refused %s: %+v", o.Path, o)
		}
	}
	if len(rec.calls)+len(rec2.calls) != 0 {
		t.Fatalf("sink called: %+v %+v", rec.calls, rec2.calls)
	}
	got := blockBodies(t, []byte(slurp(t, repo, ".codex/config.toml")), FormHash)
	if got["projector-x"] != projKeys || got[CodexConfigBlock] == "" {
		t.Fatalf("projector block lost on re-render: %q", got)
	}
	for _, e := range m3.Entries {
		for _, b := range e.Blocks {
			if b.ID == "projector-x" && b.GeneratorID != "projector-x" {
				t.Fatalf("projector block owner = %q", b.GeneratorID)
			}
		}
	}
}

func TestRenderScopedHarnessRefusesBadScopes(t *testing.T) {
	isolate(t)
	cases := map[string][]ScopeView{
		"generate: policy view has no scope":                   nil,
		`generate: scope "." has slug "x", want "root"`:        {{Slug: "x", Rel: "."}},
		`generate: two scopes share slug "root"`:               {{Slug: "root", Rel: "."}, {Slug: "root", Rel: "."}},
		`generate: scope "../up" is not a clean relative path`: {{Slug: "up", Rel: "../up"}},
		`generate: scope "/abs" is not a clean relative path`:  {{Slug: "abs", Rel: "/abs"}},
	}
	for msg, scopes := range cases {
		_, err := RenderScopedHarness(View{Scopes: scopes})
		wantKind(t, err, cascade.KindInvalidInput, msg)
	}
	long := strings.Repeat("a", 60)
	_, err := RenderScopedHarness(View{Scopes: []ScopeView{{Slug: long, Rel: long}}})
	wantKind(t, err, cascade.KindInvalidInput, `generate: scope slug "`+long+`" makes no valid block id`)
}

func TestRenderScopedHarnessNestedScope(t *testing.T) {
	isolate(t)
	v := fixtureView(t)
	v.Scopes = append(v.Scopes, ScopeView{Slug: "apps-web", Rel: "apps/web"})
	rs, err := RenderScopedHarness(v)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(rs))
	for i, r := range rs {
		paths[i] = r.Path
	}
	want := []string{"AGENTS.md", ".codex/config.toml", "opencode.json", "apps/web/.codex/config.toml", "apps/web/opencode.json"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
	blocks := blocksOf(t, rs[0].Content)
	if len(blocks) != 2 || !strings.Contains(blocks["agents-md-apps-web"], "PAC Tier") {
		t.Fatalf("nested scope block = %q", blocks)
	}
	if got := MCPServeArgv(); &got[0] == &MCPServeArgv()[0] {
		t.Fatal("MCPServeArgv shares its backing array")
	}
	if _, err := os.Stat(filepath.Join("testdata", "golden", "harness")); err != nil {
		t.Fatal(err)
	}
}
