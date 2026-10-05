package generate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// standIn builds a stand-in for an owner's ~/.claude under the fake home.
func standIn(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "keep.md", "owner global config\n")
	return dir
}

// symlink makes link point at to, failing the test when the platform cannot.
func symlink(t *testing.T, to, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// escapes asserts out/err describe a path-escape refusal with no sidecar.
func escapes(t *testing.T, out Outcome, err error, rec *recorder, path string) {
	t.Helper()
	if err == nil || !isPathEscape(err) {
		t.Fatalf("%s: err = %v, want path-escape", path, err)
	}
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
		t.Fatalf("%s: kind = %v", path, k)
	}
	if out.Written || out.Sidecar != "" || out.Reason != ReasonPathEscape {
		t.Fatalf("%s: outcome = %+v", path, out)
	}
	if len(rec.calls) != 1 || rec.calls[0] != (sinkCall{path, "", ReasonPathEscape}) {
		t.Fatalf("%s: sink calls = %+v", path, rec.calls)
	}
}

func TestWriterRefusesSymlinkedDirEscape(t *testing.T) {
	for _, dir := range []string{".claude", ".cascade", ".codex"} {
		t.Run(dir, func(t *testing.T) {
			repo, home := newRepo(t)
			outside := standIn(t, home, "global"+dir)
			symlink(t, outside, filepath.Join(repo, dir))
			beforeOutside, beforeRepo := snapshot(t, outside), snapshot(t, repo)
			rec := &recorder{}
			w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
			path := dir + "/rules/root.md"
			out, err := w.Apply(context.Background(), Rendered{Path: path, GeneratorID: genA, Content: []byte(baseFile())})
			escapes(t, out, err, rec, path)
			sameTree(t, beforeOutside, snapshot(t, outside))
			sameTree(t, beforeRepo, snapshot(t, repo))
			if entries, _ := os.ReadDir(home); len(entries) != 1 {
				t.Fatalf("something was created in the fake home: %v", entries)
			}
		})
	}
	t.Run("symlink that stays inside is refused too", func(t *testing.T) {
		repo, _ := newRepo(t)
		put(t, repo, "real/keep.md", "x\n")
		symlink(t, filepath.Join(repo, "real"), filepath.Join(repo, ".claude"))
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
		out, err := w.Apply(context.Background(), Rendered{Path: ".claude/CLAUDE.md", GeneratorID: genA, Content: []byte(baseFile())})
		escapes(t, out, err, rec, ".claude/CLAUDE.md")
		if !missing(repo, "real/CLAUDE.md") {
			t.Fatal("wrote through an in-repo symlink")
		}
	})
}

func TestWriterRefusesSymlinkTarget(t *testing.T) {
	repo, home := newRepo(t)
	outside := standIn(t, home, "global")
	symlink(t, filepath.Join(outside, "keep.md"), filepath.Join(repo, "AGENTS.md"))
	beforeOutside := snapshot(t, outside)
	rec := &recorder{}
	w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
	out, err := w.Apply(context.Background(), Rendered{Path: "AGENTS.md", GeneratorID: genA, Content: []byte(baseFile())})
	escapes(t, out, err, rec, "AGENTS.md")
	sameTree(t, beforeOutside, snapshot(t, outside))
	if !missing(repo, "AGENTS.md.cascade-new") {
		t.Fatal("a sidecar was written for a path-escape")
	}
	if to, err := os.Readlink(filepath.Join(repo, "AGENTS.md")); err != nil || to != filepath.Join(outside, "keep.md") {
		t.Fatalf("the symlink itself was changed: %v %v", to, err)
	}
}

func TestWriterRefusesDotDotAndAbsolute(t *testing.T) {
	repo, home := newRepo(t)
	outside := standIn(t, home, "global")
	for _, path := range []string{"../escape.md", "a/../../escape.md", "a/../b.md", filepath.Join(outside, "x.md"), "/etc/x.md", `a\..\x.md`, ".", ""} {
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
		out, err := w.Apply(context.Background(), Rendered{Path: path, GeneratorID: genA, Content: []byte(baseFile())})
		escapes(t, out, err, rec, path)
		rec = &recorder{}
		w, _ = NewWriter(repo, rec.sink(), Manifest{}, false)
		out, err = w.ApplyBlock(context.Background(), path, genA, "b", []byte("x\n"))
		escapes(t, out, err, rec, path)
	}
	if !missing(filepath.Dir(repo), "escape.md") || len(snapshot(t, outside)) != 2 {
		t.Fatal("a file appeared outside the repository")
	}
}

func TestManifestDirSymlinkRefused(t *testing.T) {
	repo, home := newRepo(t)
	outside := standIn(t, home, "global.cascade")
	symlink(t, outside, filepath.Join(repo, ".cascade"))
	before := snapshot(t, outside)
	if _, ok, err := ReadManifest(repo); ok || err == nil || !isPathEscape(err) {
		t.Fatalf("ReadManifest: ok=%v err=%v", ok, err)
	}
	if err := WriteManifest(repo, Manifest{}); err == nil || !isPathEscape(err) {
		t.Fatalf("WriteManifest err = %v", err)
	}
	if _, err := Check(context.Background(), repo); err == nil || !isPathEscape(err) {
		t.Fatalf("Check err = %v", err)
	}
	sameTree(t, before, snapshot(t, outside))
}

func TestConfineRejectsOddTargets(t *testing.T) {
	repo, _ := newRepo(t)
	put(t, repo, "file", "x\n")
	if err := os.MkdirAll(filepath.Join(repo, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, msg := range map[string]string{
		"file/child.md": "generate: file is not a directory",
		"dir.md":        "generate: dir.md is not a regular file",
	} {
		_, err := openConfined(repo, path)
		wantKind(t, err, cascade.KindInvalidInput, msg)
	}
	if _, err := openConfined("", "a.md"); err == nil {
		t.Fatal("empty root accepted")
	}
	_, err := openConfined(filepath.Join(repo, "nope"), "a.md")
	if k, _ := cascade.KindOf(err); k != cascade.KindNotFound {
		t.Fatalf("missing root kind = %v", k)
	}
}
