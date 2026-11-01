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
)

// wantWorkflowTemplates is the closed embedded workflows set.
var wantWorkflowTemplates = []string{
	"templates/workflows/implement.js.tmpl",
	"templates/workflows/review.js.tmpl",
	"templates/workflows/verify.js.tmpl",
}

func TestTemplateHashesCoverWorkflowTemplates(t *testing.T) {
	hashes := TemplateHashes()
	var got []string
	for k := range hashes {
		if strings.HasPrefix(k, "templates/workflows/") {
			got = append(got, k)
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(wantWorkflowTemplates, "\n") {
		t.Fatalf("TemplateHashes workflows keys =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantWorkflowTemplates, "\n"))
	}
	if disk := diskFiles(t, "templates/workflows"); strings.Join(disk, "\n") != strings.Join(got, "\n") {
		t.Fatalf("on-disk workflows templates =\n%s\nembedded =\n%s", strings.Join(disk, "\n"), strings.Join(got, "\n"))
	}
	for _, k := range got {
		emb, err := fs.ReadFile(TemplateFS(), k)
		if err != nil {
			t.Fatal(err)
		}
		disk, err := os.ReadFile(filepath.FromSlash(k))
		if err != nil || string(disk) != string(emb) {
			t.Fatalf("%s: embedded bytes differ from the source (%v)", k, err)
		}
		sum := sha256.Sum256(emb)
		if hashes[k] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s hash = %s, want the sha256 of its bytes", k, hashes[k])
		}
	}
	if _, err := fs.Stat(TemplateFS(), "templates/wiki-sync-workflow.yml"); err == nil {
		t.Fatal("the wiki workflow template is embedded")
	}
}
