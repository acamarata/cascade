package generate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// checked generates target, writes the manifest and returns the repo.
func checked(t *testing.T) string {
	t.Helper()
	repo, _ := newRepo(t)
	if err := WriteManifest(repo, seed(t, repo)); err != nil {
		t.Fatal(err)
	}
	return repo
}

// mustRemove deletes path under repo or fails the test.
func mustRemove(t *testing.T, repo, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// checkCase is one way to turn a generated repository red.
type checkCase struct {
	name, cond string
	mutate     func(t *testing.T, repo string)
}

func checkCases() []checkCase {
	end := "<!-- cascade:managed:end -->\n"
	return []checkCase{
		{"manifest absent", "manifest-absent", func(t *testing.T, repo string) { mustRemove(t, repo, ManifestRel) }},
		{"file missing", "file-missing", func(t *testing.T, repo string) { mustRemove(t, repo, target) }},
		{"mangled marker", "mangled-marker", func(t *testing.T, repo string) {
			put(t, repo, target, strings.Replace(baseFile(), end, "", 1))
		}},
		{"managed block edited", "managed-block-edited", func(t *testing.T, repo string) {
			put(t, repo, target, strings.Replace(baseFile(), "one\n", "edited\n", 1))
		}},
		{"block deleted whole", "managed-block-edited", func(t *testing.T, repo string) {
			put(t, repo, target, "# Title\n")
		}},
	}
}

func TestCheckConditions(t *testing.T) {
	for _, c := range checkCases() {
		t.Run(c.name, func(t *testing.T) {
			repo := checked(t)
			c.mutate(t, repo)
			d, err := Check(context.Background(), repo)
			if err != nil || d.Clean() || len(d.Items) != 1 || d.Items[0].Condition != c.cond {
				t.Fatalf("drift = %+v %v, want exactly %s", d, err, c.cond)
			}
			if c.cond == "manifest-absent" && d.Items[0].Path != ManifestRel {
				t.Fatalf("path = %s", d.Items[0].Path)
			}
		})
	}
}

func TestCheckGreenAndErrors(t *testing.T) {
	if d, err := Check(context.Background(), checked(t)); err != nil || !d.Clean() {
		t.Fatalf("drift = %+v %v", d, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, checked(t)); err == nil {
		t.Fatal("canceled context accepted")
	} else if k, _ := cascade.KindOf(err); k != cascade.KindCanceled {
		t.Fatalf("kind = %v", k)
	}
	repo := checked(t)
	put(t, repo, ManifestRel, "{")
	if d, err := Check(context.Background(), repo); err == nil {
		t.Fatalf("an invalid manifest gave drift %+v with no error", d)
	}
}

func TestCheckIgnoresUnmanagedContent(t *testing.T) {
	repo := checked(t)
	for _, edit := range []string{
		strings.Replace(baseFile(), "footer\n", "my own footer\n", 1),
		"my own preface\n" + baseFile(),
		baseFile() + "\nappended by hand\n",
		strings.Replace(baseFile(), "# Title\n", "# My title\n\nprose\n", 1),
	} {
		put(t, repo, target, edit)
		d, err := Check(context.Background(), repo)
		if err != nil || !d.Clean() {
			t.Fatalf("an edit outside every marker turned Check red: %+v %v\n%s", d, err, edit)
		}
	}
}

func TestCheckWritesNothing(t *testing.T) {
	for _, mutate := range []func(*testing.T, string){
		func(*testing.T, string) {},
		func(t *testing.T, repo string) { put(t, repo, target, "# gone\n") },
		func(t *testing.T, repo string) { mustRemove(t, repo, ManifestRel) },
		func(t *testing.T, repo string) { mustRemove(t, repo, target) },
	} {
		repo := checked(t)
		mutate(t, repo)
		before := snapshot(t, repo)
		if _, err := Check(context.Background(), repo); err != nil {
			t.Fatal(err)
		}
		sameTree(t, before, snapshot(t, repo))
	}
	empty, _ := newRepo(t)
	if _, err := Check(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Fatalf("Check created %d entries in an empty repository", len(entries))
	}
}
