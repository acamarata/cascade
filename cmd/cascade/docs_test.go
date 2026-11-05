package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/internal/tools/docgen"
	"github.com/acamarata/cascade/pkg/cascade"
)

// docsWorkdir makes a temp working directory holding ./docs and enters it.
func docsWorkdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func docsWantKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if !ok || got != want {
		t.Fatalf("kind = %v (ok=%v), want %v; err=%v", got, ok, want, err)
	}
}

func TestDocsGenErrorPaths(t *testing.T) {
	t.Run("no docs dir", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		_, err := execRoot(t, "docs", "gen")
		docsWantKind(t, err, cascade.KindNotFound)
		if !strings.Contains(err.Error(), "docs") {
			t.Errorf("error does not name docs: %v", err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("wrote %d entries despite missing docs", len(entries))
		}
	})
	t.Run("no docs dir --json", func(t *testing.T) {
		t.Chdir(t.TempDir())
		out, err := execRoot(t, "--json", "docs", "gen")
		docsWantKind(t, err, cascade.KindNotFound)
		if strings.Contains(out, `"ok":true`) {
			t.Errorf("--json printed a success envelope on failure: %s", out)
		}
		env := output.NewErrEnvelope(err)
		if env.OK || env.Error == nil || env.Error.Kind != cascade.KindNotFound.String() {
			t.Errorf("--json error envelope = %+v, want kind %s", env, cascade.KindNotFound)
		}
	})
	t.Run("unwritable docs", func(t *testing.T) {
		if goruntime.GOOS != "windows" && os.Geteuid() == 0 {
			t.Skip("root ignores mode bits")
		}
		dir := docsWorkdir(t)
		makeDocsDirUnwritable(t, filepath.Join(dir, "docs"))
		_, err := execRoot(t, "docs", "gen")
		docsWantKind(t, err, cascade.KindUnavailable)
	})
}

func TestDocsGenMounted(t *testing.T) {
	dir := docsWorkdir(t)
	out, err := execRoot(t, "--json", "docs", "gen")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Files []string `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !env.OK || len(env.Data.Files) == 0 {
		t.Fatalf("envelope = %+v", env)
	}
	for _, p := range []string{"docs/cli/cascade.md", "docs/cli/cascade_docs.md", "docs/cli/cascade_docs_gen.md", "docs/reference/config-keys.md"} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !strings.HasPrefix(string(b), docgen.OwnedMarker) {
			t.Errorf("%s lacks the owned marker", p)
		}
	}
	keys, err := os.ReadFile(filepath.Join(dir, "docs", "reference", "config-keys.md"))
	if err != nil {
		t.Fatal(err)
	}
	all := append(runtime.KnownConfigKeys(), initconfig.KnownKeys()...)
	if len(all) == 0 {
		t.Fatal("the key registries are empty")
	}
	for _, k := range all {
		if !strings.Contains(string(keys), "| `"+k+"` |") {
			t.Errorf("config-keys.md does not list registry key %q", k)
		}
	}
}

func TestDocsGenLinksGuides(t *testing.T) {
	dir := docsWorkdir(t)
	guides := filepath.Join(dir, "docs", "cli-reference")
	if err := os.MkdirAll(guides, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guides, "config.md"), []byte("# config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := emptyHomeRoot()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{}
	for _, p := range withGuides(".", docgen.RenderCommands(root)) {
		body[p.Path] = string(p.Body)
	}
	if got := body["cascade_config.md"]; !strings.Contains(got, "../cli-reference/config.md") {
		t.Errorf("cascade_config.md has no guide link:\n%s", got)
	}
	if got := body["cascade_version.md"]; got == "" || strings.Contains(got, "Guide:") {
		t.Errorf("cascade_version.md missing or has a Guide line without a guide:\n%s", got)
	}
}

func TestDocsGenStaleAndForeignFiles(t *testing.T) {
	dir := docsWorkdir(t)
	// A small tree keeps the page writes (each one synced) short; the
	// production root is exercised by TestDocsGenMounted.
	prev := docsRoot
	docsRoot = func() (*cobra.Command, error) {
		root := &cobra.Command{Use: "cascade", Short: "stub"}
		root.AddCommand(&cobra.Command{Use: "one", Short: "one", Run: func(*cobra.Command, []string) {}})
		return root, nil
	}
	t.Cleanup(func() { docsRoot = prev })
	cli := filepath.Join(dir, "docs", "cli")
	if err := os.MkdirAll(cli, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cli, "cascade_gone.md")
	if err := os.WriteFile(stale, []byte(docgen.OwnedMarker+"\n\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := execRoot(t, "docs", "gen"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale owned page survived: %v", err)
	}
	foreign := filepath.Join(cli, "README.md")
	if err := os.WriteFile(foreign, []byte("hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := execRoot(t, "docs", "gen")
	docsWantKind(t, err, cascade.KindConflict)
	if b, _ := os.ReadFile(foreign); string(b) != "hand written\n" {
		t.Errorf("foreign file was modified: %q", b)
	}
}
