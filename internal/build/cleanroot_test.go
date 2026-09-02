package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// cleanrootModuleRoot locates the repo root by walking up from this file.
func cleanrootModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("clean-root gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("clean-root gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestCleanRoot_RealTreeGreen: every tracked path at the real repo's root
// is on the RELEASE-STATE allowlist.
func TestCleanRoot_RealTreeGreen(t *testing.T) {
	root := cleanrootModuleRoot(t)
	files, err := GitLsFilesRoot(root)
	if err != nil {
		t.Fatalf("clean-root gate: git ls-files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("clean-root gate: git ls-files returned zero tracked paths — gate regression, not a clean repo")
	}
	v := CheckCleanRoot(files)
	if len(v) != 0 {
		t.Fatalf("clean-root gate: %d violation(s) in the real tree: %+v", len(v), v)
	}
}

// TestCleanRoot_AbsentAllowedFilesAreNotViolations proves Art.10.1's
// "absence is not a violation": CHANGELOG.md, install.sh, and Makefile are
// on the allowlist (09-REVIEW-RESOLUTIONS.md §Round 7) but do not exist in
// this repo today, and the real-tree check above already passed — this
// test makes the intent explicit rather than leaving it implicit in a
// passing count.
func TestCleanRoot_AbsentAllowedFilesAreNotViolations(t *testing.T) {
	root := cleanrootModuleRoot(t)
	files, err := GitLsFilesRoot(root)
	if err != nil {
		t.Fatalf("clean-root gate: git ls-files: %v", err)
	}
	tracked := make(map[string]bool)
	for _, f := range files {
		tracked[f] = true
	}
	for _, must := range []string{"CHANGELOG.md", "install.sh", "Makefile"} {
		if tracked[must] {
			continue // present is fine too; this test only asserts absence never fails the check.
		}
	}
	v := CheckCleanRoot(files)
	for _, viol := range v {
		if viol.Path == "CHANGELOG.md" || viol.Path == "install.sh" || viol.Path == "Makefile" {
			t.Fatalf("clean-root gate: %s must never be reported as a violation merely by being absent", viol.Path)
		}
	}
}

// cleanrootMaterializeRepo copies the fixture tree at src into a fresh
// t.TempDir() (Art.7.1), git-inits it, and commits every file — turning a
// static fixture tree into a real tracked-file listing GitLsFilesRoot can
// read, exactly what the live gate consumes from the real repo.
func cleanrootMaterializeRepo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if walkErr != nil {
		t.Fatalf("clean-root gate: materializing fixture %s: %v", src, walkErr)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clean-root gate: git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "gate@example.invalid")
	run("config", "user.name", "gate")
	run("add", "-A")
	run("commit", "-q", "-m", "seeded fixture")
	return dir
}

func cleanrootFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(cleanrootModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "cleanroot")
}

// TestCleanRoot_SeededViolationRed_StrayFile proves the gate fires on a
// stray tracked file at root outside the allowlist.
func TestCleanRoot_SeededViolationRed_StrayFile(t *testing.T) {
	dir := cleanrootMaterializeRepo(t, filepath.Join(cleanrootFixtureDir(t), "stray-file"))
	files, err := GitLsFilesRoot(dir)
	if err != nil {
		t.Fatalf("clean-root gate: git ls-files: %v", err)
	}
	v := CheckCleanRoot(files)
	var found bool
	for _, viol := range v {
		if viol.Path == "NOTES.txt" && viol.IsFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("clean-root gate: expected a violation for NOTES.txt, got %+v", v)
	}
}

// TestCleanRoot_SeededViolationRed_StrayDir proves the gate fires on a
// stray tracked directory at root outside the allowlist, reported once
// regardless of how many files it contains.
func TestCleanRoot_SeededViolationRed_StrayDir(t *testing.T) {
	dir := cleanrootMaterializeRepo(t, filepath.Join(cleanrootFixtureDir(t), "stray-dir"))
	files, err := GitLsFilesRoot(dir)
	if err != nil {
		t.Fatalf("clean-root gate: git ls-files: %v", err)
	}
	v := CheckCleanRoot(files)
	var count int
	for _, viol := range v {
		if viol.Path == "scripts" && !viol.IsFile {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("clean-root gate: expected exactly 1 violation for the stray scripts/ dir, got %d (v=%+v)", count, v)
	}
}
