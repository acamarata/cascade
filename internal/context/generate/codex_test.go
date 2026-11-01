package generate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	cctx "github.com/acamarata/cascade/internal/context"
)

// harnessGoldens maps each scoped harness path to its golden under
// testdata/golden/harness/.
var harnessGoldens = map[string]string{
	"AGENTS.md":          "AGENTS.md",
	".codex/config.toml": "codex-config.toml",
	"opencode.json":      "opencode.json",
}

// renderHarness renders the fixture view's scoped harness files by path.
func renderHarness(t *testing.T) ([]Rendered, map[string]Rendered) {
	t.Helper()
	rs, err := RenderScopedHarness(fixtureView(t))
	if err != nil {
		t.Fatalf("RenderScopedHarness: %v", err)
	}
	by := map[string]Rendered{}
	for _, r := range rs {
		by[r.Path] = r
	}
	return rs, by
}

// goldenBytes reads one harness golden.
func goldenBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "harness", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// blockBodies parses content in form and maps block id to body.
func blockBodies(t *testing.T, content []byte, form MarkerForm) map[string]string {
	t.Helper()
	bs, err := ParseManagedBlocks(content, form, nil)
	if err != nil {
		t.Fatalf("ParseManagedBlocks: %v", err)
	}
	out := map[string]string{}
	for _, b := range bs {
		out[b.ID] = string(b.Body)
	}
	return out
}

func TestCodexScopedGenerate(t *testing.T) {
	repo, _ := newRepo(t)
	rs, by := renderHarness(t)
	agents, cfg := by["AGENTS.md"], by[".codex/config.toml"]
	if agents.GeneratorID != "agents-md" || cfg.GeneratorID != "codex-config" {
		t.Fatalf("generator ids = %q, %q", agents.GeneratorID, cfg.GeneratorID)
	}
	files, err := (&cctx.CXInstructionWriter{}).Generate(scopedMerged(fixtureView(t), fixtureView(t).Scopes[0]))
	if err != nil || len(files) != 1 || files[0].Name != "AGENTS.md" {
		t.Fatalf("CXInstructionWriter: %v, %+v", err, files)
	}
	if got := blocksOf(t, agents.Content); len(got) != 1 || got["agents-md-root"] != string(files[0].Content) {
		t.Fatalf("AGENTS.md is not one agents-md-root block of the CX writer's output:\n%s", agents.Content)
	}
	const want = "[mcp_servers.cascade]\ncommand = \"cascade\"\nargs = [\"mcp\", \"serve\", \"--stdio\"]\n"
	if got := blockBodies(t, cfg.Content, FormHash); len(got) != 1 || got[CodexConfigBlock] != want {
		t.Fatalf("codex-config block = %q, want exactly the owned table", got)
	}
	if spans, _ := scanBlocks(cfg.Content, FormHash, nil); len(outsideBytes(cfg.Content, spans)) != 0 {
		t.Fatalf("config.toml has bytes outside its block:\n%s", cfg.Content)
	}
	m, outs, rec := applyAll(t, repo, Manifest{}, false, rs)
	for _, o := range outs {
		if !o.Written {
			t.Fatalf("first render not written: %+v", o)
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("sink called on a fresh repo: %+v", rec.calls)
	}
	ids := map[string]string{}
	for _, e := range m.Entries {
		ids[e.Path] = e.GeneratorID
	}
	if ids["AGENTS.md"] != "agents-md" || ids[".codex/config.toml"] != "codex-config" {
		t.Fatalf("manifest entries = %+v", m.Entries)
	}
	if slurp(t, repo, ".codex/config.toml") != string(cfg.Content) {
		t.Fatal("config.toml on disk is not the rendering")
	}
}

// capture is one real-counterpart parser run over a golden.
type capture struct {
	Tool          string `json:"tool"`
	Version       string `json:"version"`
	CapturedAt    string `json:"captured_at"`
	Command       string `json:"command"`
	GoldenSHA256  string `json:"golden_sha256"`
	StdoutExcerpt string `json:"stdout_excerpt"`
	ExitCode      int    `json:"exit_code"`
}

// loadCapture strictly decodes testdata/<dir>/accepted.json and checks
// it was taken over the current golden.
func loadCapture(t *testing.T, dir, golden, tool string) capture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", dir, "accepted.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c capture
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("decode %s capture: %v", dir, err)
	}
	sum := sha256.Sum256(goldenBytes(t, golden))
	if c.GoldenSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("%s golden_sha256 %s does not match testdata/golden/harness/%s: re-capture after changing a golden", dir, c.GoldenSHA256, golden)
	}
	if c.Tool != tool || c.Version == "" || c.CapturedAt == "" || c.ExitCode != 0 {
		t.Fatalf("%s capture = %+v", dir, c)
	}
	return c
}

func TestCodexRealParserAcceptsConfig(t *testing.T) {
	c := loadCapture(t, "real-codex", "codex-config.toml", "codex")
	if c.Command != "codex mcp list --json" {
		t.Fatalf("command = %q", c.Command)
	}
	var servers []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"transport"`
	}
	if err := json.Unmarshal([]byte(c.StdoutExcerpt), &servers); err != nil {
		t.Fatalf("stdout_excerpt is not the codex JSON listing: %v", err)
	}
	argv := MCPServeArgv()
	for _, s := range servers {
		tr := s.Transport
		if s.Name == "cascade" && s.Enabled && tr.Type == "stdio" && tr.Command == argv[0] && strings.Join(tr.Args, " ") == strings.Join(argv[1:], " ") {
			return
		}
	}
	t.Fatalf("codex did not list the cascade server over the golden: %s", c.StdoutExcerpt)
}

func TestScopedHarnessGoldens(t *testing.T) {
	isolate(t)
	rs, _ := renderHarness(t)
	if len(rs) != len(harnessGoldens) {
		t.Fatalf("rendered %d files, want %d", len(rs), len(harnessGoldens))
	}
	update := os.Getenv("CASCADE_UPDATE_HARNESS_GOLDENS") == "1"
	for _, r := range rs {
		name, ok := harnessGoldens[r.Path]
		if !ok {
			t.Fatalf("no golden for %s", r.Path)
		}
		if update {
			if err := os.WriteFile(filepath.Join("testdata", "golden", "harness", name), r.Content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if want := goldenBytes(t, name); !bytes.Equal(r.Content, want) {
			t.Fatalf("%s differs from its golden:\n--- got\n%s\n--- want\n%s", r.Path, r.Content, want)
		}
	}
}

func TestScopedByteStability(t *testing.T) {
	isolate(t)
	a, _ := renderHarness(t)
	b, _ := renderHarness(t)
	if len(a) != len(b) {
		t.Fatalf("render counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].GeneratorID != b[i].GeneratorID || !bytes.Equal(a[i].Content, b[i].Content) {
			t.Fatalf("render %d differs between calls: %s", i, a[i].Path)
		}
		if loc := timestampShape.Find(a[i].Content); loc != nil {
			t.Fatalf("%s carries a timestamp-shaped value %q", a[i].Path, loc)
		}
	}
}

// scrubShapes are what a committed capture must never carry.
var scrubShapes = []*regexp.Regexp{
	regexp.MustCompile(`/Users/`),
	regexp.MustCompile(`/home/`),
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
}

func TestRealHarnessCapturesScrubbed(t *testing.T) {
	n := 0
	for _, dir := range []string{"real-codex", "real-opencode"} {
		err := filepath.WalkDir(filepath.Join("testdata", dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			n++
			for i, re := range scrubShapes {
				if re.Match(data) {
					t.Errorf("%s matches scrub pattern #%d", p, i)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n < 4 {
		t.Fatalf("scanned %d capture files, want at least 4", n)
	}
}
