package inventory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildFixtureRepo creates a minimal git repo under t.TempDir() with a
// providers/ tree, a plugins/ tree, one tracked .go file carrying two
// SPORT lines, and a ci.yml matrix, then commits it. Every
// ComputeTreeCounts/PlatformsFromCI test below runs against this fixture,
// never against the real cascade tree (which several other agents edit
// concurrently in this phase).
func buildFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "providers", "alpha"))
	mustMkdirAll(t, filepath.Join(root, "providers", "beta"))
	mustMkdirAll(t, filepath.Join(root, "plugins", "gamma"))
	mustWriteFile(t, filepath.Join(root, "providers", "alpha", "a.go"),
		"package alpha\n\n// SPORT: fixture.alpha/ADDED.\nfunc A() {}\n")
	mustWriteFile(t, filepath.Join(root, "providers", "beta", "b.go"),
		"package beta\n\n// SPORT: fixture.beta/ADDED.\nfunc B() {}\n")
	mustMkdirAll(t, filepath.Join(root, ".github", "workflows"))
	mustWriteFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), fixtureCIYAML)

	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "fixture@example.com")
	runGit(t, root, "config", "user.name", "fixture")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "fixture")
	return root
}

const fixtureCIYAML = `
jobs:
  build-test:
    strategy:
      matrix:
        include:
          - { goos: darwin, goarch: arm64 }
          - { goos: linux, goarch: amd64 }
          - { goos: linux, goarch: arm64 }
`

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

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestComputeTreeCounts(t *testing.T) {
	root := buildFixtureRepo(t)
	tc, err := ComputeTreeCounts(root)
	if err != nil {
		t.Fatalf("ComputeTreeCounts: %v", err)
	}
	if tc.Providers != 2 {
		t.Errorf("Providers = %d, want 2", tc.Providers)
	}
	if tc.Plugins != 1 {
		t.Errorf("Plugins = %d, want 1", tc.Plugins)
	}
	if tc.SPORTLines != 2 {
		t.Errorf("SPORTLines = %d, want 2", tc.SPORTLines)
	}
	if len(tc.Platforms) != 2 || tc.Platforms[0] != "darwin" || tc.Platforms[1] != "linux" {
		t.Errorf("Platforms = %v, want [darwin linux]", tc.Platforms)
	}
}

func TestComputeTreeCounts_MissingProvidersDir(t *testing.T) {
	root := t.TempDir()
	if _, err := ComputeTreeCounts(root); err == nil {
		t.Fatal("ComputeTreeCounts: want error for missing providers/, got nil")
	}
}

func TestPlatformsFromCI_MissingFile(t *testing.T) {
	root := t.TempDir()
	if _, err := PlatformsFromCI(root); err == nil {
		t.Fatal("PlatformsFromCI: want error for missing ci.yml, got nil")
	}
}

func TestPlatformsFromCI_NoMatrixEntries(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, ".github", "workflows"))
	mustWriteFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "jobs: {}\n")
	if _, err := PlatformsFromCI(root); err == nil {
		t.Fatal("PlatformsFromCI: want error for empty matrix, got nil")
	}
}
