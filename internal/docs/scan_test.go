package docs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmptyRepoList(t *testing.T) {
	result, err := Scan([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil map")
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(result))
	}
}

func TestNestedDocsCollected(t *testing.T) {
	dir := t.TempDir()
	// depth 1: dir/a.md
	writeFile(t, filepath.Join(dir, "a.md"))
	// depth 2: dir/sub/b.md
	writeFile(t, filepath.Join(dir, "sub", "b.md"))
	// depth 3: dir/sub/deep/c.md
	writeFile(t, filepath.Join(dir, "sub", "deep", "c.md"))

	result, err := Scan([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries := result[dir]
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	depths := map[int]bool{}
	for _, e := range entries {
		depths[e.Depth] = true
	}
	for _, d := range []int{1, 2, 3} {
		if !depths[d] {
			t.Errorf("missing entry at depth %d", d)
		}
	}
}

func TestSingleRepoDepthLimit(t *testing.T) {
	dir := t.TempDir()
	// Create a 5-level deep tree
	// depth 1: d1.md
	writeFile(t, filepath.Join(dir, "d1.md"))
	// depth 2: l1/d2.md
	writeFile(t, filepath.Join(dir, "l1", "d2.md"))
	// depth 3: l1/l2/d3.md
	writeFile(t, filepath.Join(dir, "l1", "l2", "d3.md"))
	// depth 4: l1/l2/l3/d4.md
	writeFile(t, filepath.Join(dir, "l1", "l2", "l3", "d4.md"))
	// depth 5: l1/l2/l3/l4/d5.md (should be excluded)
	writeFile(t, filepath.Join(dir, "l1", "l2", "l3", "l4", "d5.md"))

	result, err := Scan([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries := result[dir]
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Depth > 4 {
			t.Errorf("entry at depth %d exceeds limit: %s", e.Depth, e.Path)
		}
	}
}

func TestMultiRepoScanStopsAtDepth4(t *testing.T) {
	r1 := t.TempDir()
	r2 := t.TempDir()

	// repo 1: 5 levels deep
	writeFile(t, filepath.Join(r1, "a.md"))
	writeFile(t, filepath.Join(r1, "l1", "b.md"))
	writeFile(t, filepath.Join(r1, "l1", "l2", "c.md"))
	writeFile(t, filepath.Join(r1, "l1", "l2", "l3", "d.md"))
	writeFile(t, filepath.Join(r1, "l1", "l2", "l3", "l4", "e.md"))

	// repo 2: 5 levels deep
	writeFile(t, filepath.Join(r2, "x.md"))
	writeFile(t, filepath.Join(r2, "m1", "y.md"))
	writeFile(t, filepath.Join(r2, "m1", "m2", "z.md"))
	writeFile(t, filepath.Join(r2, "m1", "m2", "m3", "w.md"))
	writeFile(t, filepath.Join(r2, "m1", "m2", "m3", "m4", "v.md"))

	result, err := Scan([]string{r1, r2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, repo := range []string{r1, r2} {
		entries := result[repo]
		if len(entries) != 4 {
			t.Errorf("repo %s: expected 4 entries, got %d", repo, len(entries))
		}
		for _, e := range entries {
			if e.Depth > 4 {
				t.Errorf("repo %s: entry at depth %d exceeds limit: %s", repo, e.Depth, e.Path)
			}
		}
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
