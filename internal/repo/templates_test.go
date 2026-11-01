package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// wantTemplates is the closed embedded set: four agents and five skills.
var wantTemplates = []string{
	"templates/agents/adversarial-reviewer.md",
	"templates/agents/architecture-reviewer.md",
	"templates/agents/security-reviewer.md",
	"templates/agents/test-engineer.md",
	"templates/skills/cascade-execution/SKILL.md",
	"templates/skills/delegate/SKILL.md",
	"templates/skills/init/SKILL.md",
	"templates/skills/plan/SKILL.md",
	"templates/skills/verify/SKILL.md",
}

// embeddedFiles lists every regular file of the embedded tree, sorted.
func embeddedFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(TemplateFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// diskFiles lists the regular files under the on-disk template roots.
func diskFiles(t *testing.T, roots ...string) []string {
	t.Helper()
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				out = append(out, filepath.ToSlash(p))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(out)
	return out
}

func TestTemplatesEmbeddedOnly(t *testing.T) {
	got := embeddedFiles(t)
	if strings.Join(got, "\n") != strings.Join(wantTemplates, "\n") {
		t.Fatalf("embedded templates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantTemplates, "\n"))
	}
	disk := diskFiles(t, "templates/agents", "templates/skills")
	if strings.Join(disk, "\n") != strings.Join(got, "\n") {
		t.Fatalf("on-disk agents and skills =\n%s\nembedded =\n%s", strings.Join(disk, "\n"), strings.Join(got, "\n"))
	}
	if _, err := fs.Stat(TemplateFS(), "templates/wiki-sync-workflow.yml"); err == nil {
		t.Fatal("the wiki workflow template is embedded")
	}
	if _, err := os.Stat("templates/wiki-sync-workflow.yml"); err != nil {
		t.Fatalf("the wiki workflow template left its place: %v", err)
	}
	for _, p := range got {
		disk, err := os.ReadFile(filepath.FromSlash(p))
		if err != nil {
			t.Fatal(err)
		}
		emb, err := fs.ReadFile(TemplateFS(), p)
		if err != nil || string(emb) != string(disk) {
			t.Fatalf("%s: embedded bytes differ from the source (%v)", p, err)
		}
	}
}

func TestTemplateHashesCoverEmbeddedSet(t *testing.T) {
	hashes := TemplateHashes()
	var keys []string
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, "\n") != strings.Join(wantTemplates, "\n") {
		t.Fatalf("TemplateHashes keys =\n%s\nwant\n%s", strings.Join(keys, "\n"), strings.Join(wantTemplates, "\n"))
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "templates/agents/") && !strings.HasPrefix(k, "templates/skills/") {
			t.Errorf("TemplateHashes covers %s outside the agents and skills trees", k)
		}
		data, err := fs.ReadFile(TemplateFS(), k)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hashes[k] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s hash = %s, want the sha256 of its bytes", k, hashes[k])
		}
	}
	hashes["templates/agents/extra.md"] = "x"
	if _, ok := TemplateHashes()["templates/agents/extra.md"]; ok {
		t.Fatal("TemplateHashes returns a shared map a caller can change")
	}
}

func TestHashTreeSkipsDirectoriesAndStopsOnError(t *testing.T) {
	m := fstest.MapFS{"a/b.md": {Data: []byte("x")}, "c.md": {Data: []byte("")}}
	got := hashTree(m)
	empty := sha256.Sum256(nil)
	if len(got) != 2 || got["c.md"] != hex.EncodeToString(empty[:]) {
		t.Fatalf("hashTree = %v", got)
	}
	if got := hashTree(brokenFS{}); len(got) != 0 {
		t.Fatalf("hashTree over an unreadable tree = %v, want empty", got)
	}
}

// brokenFS fails every open.
type brokenFS struct{}

func (brokenFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }
