package build

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// materializeFixtureGitRepo copies srcDir (a testdata fixture) into a
// fresh git repo under t.TempDir() and commits it, so gitLsFilesFn (the
// real implementation, not a test double) sees exactly this fixture's
// tracked files. Mirrors hook_test.go's materializeTrackedRepo pattern.
func materializeFixtureGitRepo(t *testing.T, srcDir string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(srcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("materializeFixtureGitRepo: %v", err)
	}
	runGit(t, dst, "init", "-q", "-b", "main")
	runGit(t, dst, "config", "user.email", "fixture@example.invalid")
	runGit(t, dst, "config", "user.name", "Fixture")
	runGit(t, dst, "add", "-A")
	runGit(t, dst, "commit", "-q", "-m", "chore: seed doctruth fixture")
	return dst
}

func doctruthFixtureDir(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	return filepath.Join(wd, "testdata", "seeded-violations", "doctruth", name)
}

// TestDocTruth_SeededViolationsRed proves acceptance [0]: the seeded
// fixture holds exactly one violation per rule and CheckDocTruth returns
// exactly those nine findings (set equality on Rule and the EXACT
// File:Line the fixture places each violation at — testdata/seeded-
// violations/doctruth/violations/README.md and .github/wiki/Home.md).
// A wrong line number (the fixture reflowed, or the gate mis-locating a
// finding) must fail this test, not just a wrong file or rule.
func TestDocTruth_SeededViolationsRed(t *testing.T) {
	repo := materializeFixtureGitRepo(t, doctruthFixtureDir(t, "violations"))
	rep, err := CheckDocTruth(repo, DocTruthRelease)
	if err != nil {
		t.Fatalf("CheckDocTruth: %v", err)
	}
	want := map[string]bool{
		"README.md|3|link":             true,
		"README.md|7|anchor":           true,
		"README.md|9|path":             true,
		"README.md|11|line":            true,
		"README.md|13|symbol":          true,
		"README.md|15|test":            true,
		"README.md|17|claim":           true,
		"README.md|19|directive":       true,
		".github/wiki/Home.md|0|index": true,
	}
	got := map[string]bool{}
	for _, f := range rep.New {
		key := f.File + "|" + strconv.Itoa(f.Line) + "|" + string(f.Rule)
		got[key] = true
	}
	if len(got) != len(want) {
		t.Fatalf("finding shape mismatch: got %d distinct (rule,file,line) buckets %v, want %d %v", len(got), keysOf(got), len(want), keysOf(want))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing expected finding %s; findings: %+v", k, rep.New)
		}
	}
	if len(rep.New) != 9 {
		t.Fatalf("New = %d findings, want exactly 9: %+v", len(rep.New), rep.New)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDocTruth_CleanFixtureGreen proves acceptance [0]'s second clause:
// the clean fixture returns zero findings with Files > 0 and Links > 0.
func TestDocTruth_CleanFixtureGreen(t *testing.T) {
	repo := materializeFixtureGitRepo(t, doctruthFixtureDir(t, "clean"))
	rep, err := CheckDocTruth(repo, DocTruthRelease)
	if err != nil {
		t.Fatalf("CheckDocTruth: %v", err)
	}
	if len(rep.New) != 0 {
		t.Fatalf("clean fixture: got %d findings, want 0: %+v", len(rep.New), rep.New)
	}
	if rep.Files == 0 {
		t.Fatal("clean fixture: Files == 0")
	}
	if rep.Links == 0 {
		t.Fatal("clean fixture: Links == 0")
	}
}
