package main

// Purpose: a real, git-tracked fixture tree for this package's tests,
//
//	mirroring internal/inventory/tree_test.go's own buildFixtureRepo:
//	writeCounts and writeRegistry both call through to gitTrackedGoFiles
//	(internal/inventory/tree.go, internal/inventory/sport/tree.go), which
//	shells out to `git ls-files`, so a fixture tree must itself be a real
//	git checkout, not just a directory of files (Art.2 real counterpart).
//
// SPORT: internal.inventory.gen/ADDED (tests).

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const fixtureCIYAML = `
jobs:
  build-test:
    strategy:
      matrix:
        include:
          - { goos: darwin, goarch: arm64 }
          - { goos: linux, goarch: amd64 }
`

// buildGenFixtureRepo builds a minimal but real git repo under t.TempDir()
// with everything writeCounts/writeRegistry's call chains need: a
// providers/ dir, a plugins/ dir, .github/workflows/ci.yml, one tracked
// .go file per dir carrying a well-formed SPORT marker, and the output
// directories (internal/inventory/, internal/inventory/sport/) writeCounts
// and writeRegistry write into.
func buildGenFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "providers", "alpha"))
	mustMkdirAll(t, filepath.Join(root, "plugins", "gamma"))
	mustMkdirAll(t, filepath.Join(root, ".github", "workflows"))
	mustMkdirAll(t, filepath.Join(root, "internal", "inventory", "sport"))

	mustWriteFile(t, filepath.Join(root, "providers", "alpha", "a.go"),
		"package alpha\n\n// SPORT: fixture.alpha/ADDED.\nfunc A() {}\n")
	mustWriteFile(t, filepath.Join(root, "plugins", "gamma", "g.go"),
		"package gamma\n\n// SPORT: fixture.gamma/ADDED.\nfunc G() {}\n")
	mustWriteFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), fixtureCIYAML)

	runGitGen(t, root, "init", "-q")
	runGitGen(t, root, "config", "user.email", "fixture@example.com")
	runGitGen(t, root, "config", "user.name", "fixture")
	runGitGen(t, root, "add", ".")
	runGitGen(t, root, "commit", "-q", "-m", "fixture")
	return root
}

// addMalformedSPORTFile writes an untracked-then-committed .go file whose
// SPORT marker has no extractable entity name, so a registry render over
// root fails closed rather than silently dropping the line.
func addMalformedSPORTFile(t *testing.T, root string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(root, "providers", "alpha", "bad.go"),
		"package alpha\n\n// SPORT: CHANGED.\nfunc Bad() {}\n")
	runGitGen(t, root, "add", ".")
	runGitGen(t, root, "commit", "-q", "-m", "malformed marker")
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitGen(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
