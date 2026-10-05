package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestManifestRoundTrip(t *testing.T) {
	repo, _ := newRepo(t)
	content := mdFile("main", "one\n", "footer\n")
	m := generated(t, repo, Rendered{Path: "AGENTS.md", GeneratorID: "gen-a", Content: []byte(content)})
	if err := WriteManifest(repo, m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadManifest(repo)
	if err != nil || !ok {
		t.Fatalf("ReadManifest = %v %v", ok, err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d", len(got.Entries))
	}
	e := got.Entries[0]
	if e.Path != "AGENTS.md" || e.GeneratorID != "gen-a" || e.FileSHA256 != sum(content) {
		t.Fatalf("entry = %+v", e)
	}
	if len(e.Blocks) != 1 || e.Blocks[0].ID != "main" || e.Blocks[0].ContentSHA256 != sum("one\n") || e.Blocks[0].GeneratorID != "gen-a" {
		t.Fatalf("blocks = %+v", e.Blocks)
	}
	if want := sum("# Title\n\n\nfooter\n"); e.OutsideSHA256 != want {
		t.Fatalf("outside = %s want %s", e.OutsideSHA256, want)
	}
	if got.GeneratedAt != "2026-10-05T12:00:00Z" || got.Version != ManifestVersion {
		t.Fatalf("header = %+v", got)
	}
}

func TestManifestCanonical(t *testing.T) {
	repo, _ := newRepo(t)
	h := sum("x")
	m := Manifest{GeneratedAt: "2026-10-05T12:00:00Z",
		Entries: []Entry{
			{Path: "b.md", GeneratorID: "g", FileSHA256: h, OutsideSHA256: h, Blocks: []BlockHash{
				{ID: "z", GeneratorID: "g", ContentSHA256: h}, {ID: "a", GeneratorID: "g", ContentSHA256: h}}},
			{Path: "a.md", GeneratorID: "g", FileSHA256: h, OutsideSHA256: h},
		},
		Deferrals: []Deferral{{Generator: "y", Result: "deferred"}, {Generator: "x", Result: "deferred"}}}
	if err := WriteManifest(repo, m); err != nil {
		t.Fatal(err)
	}
	first := slurp(t, repo, ManifestRel)
	if err := WriteManifest(repo, m); err != nil {
		t.Fatal(err)
	}
	if second := slurp(t, repo, ManifestRel); first != second {
		t.Fatal("two writes of equal input differ")
	}
	ia, ib := strings.Index(first, `"a.md"`), strings.Index(first, `"b.md"`)
	iblA, iblZ := strings.Index(first, `"id": "a"`), strings.Index(first, `"id": "z"`)
	ix, iy := strings.Index(first, `"generator": "x"`), strings.Index(first, `"generator": "y"`)
	if ia >= ib || iblA >= iblZ || ix >= iy {
		t.Fatalf("not sorted:\n%s", first)
	}
	if !strings.HasPrefix(first, "{\n  \"version\": 1,\n  \"generated_at\"") || !strings.HasSuffix(first, "}\n") || strings.Contains(first, "\r") {
		t.Fatalf("bad indent or line ends:\n%s", first)
	}
	if !strings.Contains(first, `"blocks": []`) {
		t.Fatalf("empty blocks must encode as []:\n%s", first)
	}
	if info, err := os.Stat(filepath.Join(repo, filepath.FromSlash(ManifestRel))); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o644) {
		t.Fatalf("mode = %v %v", info, err)
	}
}

func TestManifestRejectsInvalid(t *testing.T) {
	h := sum("x")
	entry := `{"path":"a.md","generator_id":"g","file_sha256":"` + h + `","outside_sha256":"` + h + `","blocks":[]}`
	cases := map[string]string{
		"unknown field":  `{"version":1,"generated_at":"","entries":[],"extra":1}`,
		"version 2":      `{"version":2,"generated_at":"","entries":[]}`,
		"version 0":      `{"generated_at":"","entries":[]}`,
		"trailing data":  `{"version":1,"entries":[]} {}`,
		"not json":       `nope`,
		"bad hash":       `{"version":1,"entries":[{"path":"a.md","generator_id":"g","file_sha256":"zz","outside_sha256":"zz","blocks":[]}]}`,
		"duplicate path": `{"version":1,"entries":[` + entry + `,` + entry + `]}`,
		"path escape":    `{"version":1,"entries":[{"path":"../a.md","generator_id":"g","file_sha256":"` + h + `","outside_sha256":"` + h + `","blocks":[]}]}`,
		"bad block id":   `{"version":1,"entries":[{"path":"a.md","generator_id":"g","file_sha256":"` + h + `","outside_sha256":"` + h + `","blocks":[{"id":"Bad Id","generator_id":"g","content_sha256":"` + h + `"}]}]}`,
		"bad timestamp":  `{"version":1,"generated_at":"yesterday","entries":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			repo, _ := newRepo(t)
			put(t, repo, ManifestRel, body)
			_, ok, err := ReadManifest(repo)
			if ok || err == nil {
				t.Fatalf("accepted: ok=%v err=%v", ok, err)
			}
			if k, has := cascade.KindOf(err); !has || k != cascade.KindInvalidInput {
				t.Fatalf("kind = %v, want invalid-input (%v)", k, err)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		repo, _ := newRepo(t)
		m, ok, err := ReadManifest(repo)
		if ok || err != nil || len(m.Entries) != 0 {
			t.Fatalf("absent = %+v %v %v", m, ok, err)
		}
	})
	t.Run("write refuses an unreadable manifest", func(t *testing.T) {
		repo, _ := newRepo(t)
		err := WriteManifest(repo, Manifest{Entries: []Entry{{Path: "a.md"}}})
		if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindInvalidInput || !missing(repo, ManifestRel) {
			t.Fatalf("err=%v, manifest written=%v", err, !missing(repo, ManifestRel))
		}
	})
}

func FuzzReadManifest(f *testing.F) {
	h := sum("x")
	f.Add([]byte(`{"version":1,"generated_at":"2026-10-05T12:00:00Z","entries":[{"path":"a.md","generator_id":"g","file_sha256":"` + h + `","outside_sha256":"` + h + `","blocks":[]}]}`))
	f.Add([]byte(`{"version":2}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"version":1,"entries":null}{}`))
	repo := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.MkdirAll(filepath.Join(repo, ".cascade"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(ManifestRel)), data, 0o644); err != nil {
			t.Fatal(err)
		}
		m, ok, err := ReadManifest(repo)
		if err != nil || !ok {
			return
		}
		if m.Version != ManifestVersion {
			t.Fatalf("accepted version %d", m.Version)
		}
		if _, err := encodeManifest(m); err != nil {
			t.Fatalf("accepted manifest does not re-encode: %v", err)
		}
	})
}
