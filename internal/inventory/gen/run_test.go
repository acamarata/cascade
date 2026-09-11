package main

// Purpose: run and repoRoot's own tests. Both chdir the process (git rev-
//
//	parse --show-toplevel resolves against the working directory, and
//	repoRoot/run take no root parameter), so neither test here may run
//	with t.Parallel(), and each restores the original working directory
//	via t.Cleanup before returning.
//
// Constraints: reads back through repoRoot()'s OWN resolved path, never
//
//	through the t.TempDir() string a test happened to chdir from — on
//	macOS that string is frequently a symlink alias (/var/folders/... ->
//	/private/var/folders/...), and git resolves it to the real path, so
//	comparing against the original string can read back from a path that
//	looks unrelated even though both name the same directory (see this
//	repo's own "TMPDIR override masks macOS symlink bugs" lesson).
//
// SPORT: internal.inventory.gen/ADDED (tests).

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// chdirTo changes the working directory to dir and registers a cleanup
// that restores the original one, so a failing test cannot leave later
// tests in this package running from the wrong directory.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restoring cwd to %s: %v", prev, err)
		}
	})
}

func TestRun_Success(t *testing.T) {
	fixture := buildGenFixtureRepo(t)
	chdirTo(t, fixture)

	if err := run(testWriter()); err != nil {
		t.Fatalf("run: %v", err)
	}

	resolved, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot after run: %v", err)
	}
	counts, err := os.ReadFile(filepath.Join(resolved, "internal", "inventory", "counts.json"))
	if err != nil {
		t.Fatalf("reading counts.json run wrote: %v", err)
	}
	if !bytes.Contains(counts, []byte(`"providers": 1`)) {
		t.Fatalf("counts.json = %s, want providers: 1", counts)
	}
	registry, err := os.ReadFile(filepath.Join(resolved, "internal", "inventory", "sport", "registry.json"))
	if err != nil {
		t.Fatalf("reading registry.json run wrote: %v", err)
	}
	if !bytes.Contains(registry, []byte(`"fixture.alpha"`)) {
		t.Fatalf("registry.json = %s, want fixture.alpha", registry)
	}
}

// TestRun_CountsFailureSkipsRegistry proves run refuses at writeCounts and
// never reaches writeRegistry when the tree cannot even produce
// counts.json — partial generation (a fresh registry.json over a stale or
// absent counts.json) would be worse than refusing outright.
func TestRun_CountsFailureSkipsRegistry(t *testing.T) {
	fixture := buildGenFixtureRepo(t)
	if err := os.RemoveAll(filepath.Join(fixture, "providers")); err != nil {
		t.Fatalf("removing providers/: %v", err)
	}
	chdirTo(t, fixture)

	if err := run(testWriter()); err == nil {
		t.Fatal("run over a tree with no providers/: want an error, got nil")
	}
	registryPath := filepath.Join(fixture, "internal", "inventory", "sport", "registry.json")
	if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
		t.Fatalf("run wrote registry.json at %s despite writeCounts failing first", registryPath)
	}
}

// TestRun_RepoRootFailure proves run refuses when repoRoot itself cannot
// resolve a checkout (working directory outside any git repo), rather
// than falling through to writeCounts/writeRegistry with an empty or
// otherwise wrong root.
func TestRun_RepoRootFailure(t *testing.T) {
	outside := t.TempDir() // deliberately never git-init'd
	chdirTo(t, outside)

	if err := run(testWriter()); err == nil {
		t.Fatal("run outside any git checkout: want an error, got nil")
	}
}

func TestRepoRoot_Success(t *testing.T) {
	fixture := buildGenFixtureRepo(t)
	chdirTo(t, fixture)

	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	if root == "" {
		t.Fatal("repoRoot returned an empty path with a nil error")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("repoRoot's own return value %q does not contain .git: %v", root, err)
	}
}

// TestRepoRoot_OutsideAnyRepo proves repoRoot refuses (rather than
// returning an empty or misleading path) when the working directory is
// not inside any git checkout.
func TestRepoRoot_OutsideAnyRepo(t *testing.T) {
	outside := t.TempDir() // deliberately never git-init'd
	chdirTo(t, outside)

	if _, err := repoRoot(); err == nil {
		t.Fatal("repoRoot outside any git checkout: want an error, got nil")
	}
}
