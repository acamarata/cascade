package generate

// Purpose: shared fixtures for the package tests: an isolated HOME, a
//   recording AttentionSink, tiny file helpers and a tree snapshot.
// Constraints: tests never touch the real HOME. isolate points HOME,
//   USERPROFILE and CASCADE_HOME at temp directories; a test that wants a
//   stand-in for an owner's ~/.claude builds one under that fake home.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedNow is the injected clock value every test uses.
var fixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// isolate redirects every home-like variable into a temp directory and
// returns the fake home.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade-home"))
	return home
}

// newRepo returns an empty repository directory and the fake home.
func newRepo(t *testing.T) (repo, home string) {
	t.Helper()
	home = isolate(t)
	return t.TempDir(), home
}

// sinkCall is one AttentionSink invocation.
type sinkCall struct{ Target, Sidecar, Reason string }

// recorder is an AttentionSink that records its calls.
type recorder struct {
	calls []sinkCall
	err   error
}

func (r *recorder) sink() AttentionSink {
	return func(_ context.Context, target, sidecar, reason string) error {
		r.calls = append(r.calls, sinkCall{target, sidecar, reason})
		return r.err
	}
}

// put writes content under root, creating directories.
func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// slurp reads a file under root.
func slurp(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// missing reports whether nothing exists at rel under root.
func missing(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return os.IsNotExist(err)
}

// snapshot maps every path under root (including symlinks) to its content
// or link target, for before/after comparison.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			out[rel] = "-> " + to
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails when two snapshots differ.
func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	var keys []string
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b, a := before[k], after[k]; b != a {
			t.Errorf("%s changed: %q -> %q", k, b, a)
		}
	}
}

// mdFile renders a Markdown file with one managed block and static text
// outside it.
func mdFile(blockID, body, footer string) string {
	return "# Title\n\n" + string(WrapBlock(FormMarkdown, blockID, []byte(body))) + "\n" + footer
}

// wantKind asserts err is a cascade error of kind with exactly msg.
func wantKind(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	ce, ok := err.(*cascade.Error)
	if !ok {
		t.Fatalf("error %v (%T) is not a *cascade.Error", err, err)
	}
	if ce.Kind != kind || ce.Msg != msg {
		t.Fatalf("error = {%s, %q}, want {%s, %q}", ce.Kind, ce.Msg, kind, msg)
	}
}

// generated applies r with a fresh Writer over no manifest and returns the
// manifest it produced.
func generated(t *testing.T, repo string, rs ...Rendered) Manifest {
	t.Helper()
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), Manifest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if out, err := w.Apply(context.Background(), r); err != nil || !out.Written {
			t.Fatalf("Apply %s: %+v %v", r.Path, out, err)
		}
	}
	return w.Manifest(fixedNow, nil)
}
