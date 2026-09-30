// Purpose: unit tests for walk.go's file-list/matching mechanics: the
// walk-root and exclusion rules, boundary-respecting term matching, and
// Sweeper.Scan. Split out of sweep_test.go purely to keep both test files
// under the 300-line cap (Art.10.3), mirroring the sweep.go/walk.go split.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// --- excluded / underRoot / filterTracked ---

func TestExcluded_SkipsTestdataDotClaudeChangelogAndOwnOutputs(t *testing.T) {
	for _, rel := range []string{
		"internal/foo/testdata/x.go", "internal/.claude/notes.md",
		"docs/migration/v1-pointer-inventory.md", "docs/migration/unresolved.json",
		"CHANGELOG.md", "apps/CHANGELOG.md",
	} {
		if !excluded(rel) {
			t.Errorf("excluded(%q) = false, want true", rel)
		}
	}
	if excluded("internal/foo/bar.go") {
		t.Error("excluded(internal/foo/bar.go) = true, want false")
	}
}

func TestFilterTracked_CoversRootsAndSkipsUntrackedShapedPaths(t *testing.T) {
	got := filterTracked([]string{
		"README.md", ".github/wiki/p.md", "apps/a.go", "cmd/x.go",
		"internal/y.go", "internal/y/testdata/z.go", "CHANGELOG.md",
		"scripts/outside.sh", "docs/migration/unresolved.json",
	})
	want := map[string]bool{"README.md": true, ".github/wiki/p.md": true, "apps/a.go": true, "cmd/x.go": true, "internal/y.go": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected entry %q", g)
		}
	}
}

// --- findTermMatches (the boundary rule) ---

func TestTermPattern_MatchesMarkerTableAndProse(t *testing.T) {
	if len(findTermMatches("<!-- cascade:x -->", "cascade:x")) != 1 {
		t.Error("marker form did not match")
	}
	if len(findTermMatches("| x | y |", "x")) != 1 {
		t.Error("table-cell form did not match")
	}
	if len(findTermMatches("run x now", "x")) != 1 {
		t.Error("prose form did not match")
	}
	if len(findTermMatches("xy", "x")) != 0 {
		t.Error("xy matched x, want no match")
	}
	if len(findTermMatches("x-y", "x")) != 0 {
		t.Error("x-y matched x, want no match")
	}
	if len(findTermMatches("x_y", "x")) != 0 {
		t.Error("x_y matched x, want no match")
	}
}

// --- Sweeper.Scan ---

func TestSweeperWalk_CoversRootsAndSkips(t *testing.T) {
	root := t.TempDir()
	must := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("README.md", "zz-fixture-marker here\n")
	must(".github/wiki/p.md", "zz-fixture-marker here too\n")
	must("apps/a.go", "// zz-fixture-marker\n")
	must("CHANGELOG.md", "zz-fixture-marker\n")
	must("internal/testdata/y.go", "zz-fixture-marker\n")

	terms := []Term{{Term: "zz-fixture-marker", Class: "marker"}}
	files := filterTracked([]string{"README.md", ".github/wiki/p.md", "apps/a.go", "CHANGELOG.md", "internal/testdata/y.go"})
	hits, err := (Sweeper{Root: root, Terms: terms, Files: files}).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits=%+v, want 3 (README.md, .github/wiki/p.md, apps/a.go)", hits)
	}
}
