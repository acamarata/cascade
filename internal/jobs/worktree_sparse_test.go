package jobs

// Purpose: Minimize against a REAL git repository under t.TempDir()
//
//	(Art.2): the no-cone sparse checkout keeps exactly the in-scope
//	prefixes plus declared deps, anchors each prefix at the root, refuses
//	unusable scopes, and fails closed on an out-of-scope leftover.
//
// SPORT: jobs/worktree-manager (ADD, P1-E30-W6-S62-T2).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// sparseFixtureFiles is the tracked tree every Minimize case starts from.
var sparseFixtureFiles = []string{
	"README.md", "go.mod", "src/a.go", "src/sub/b.go",
	"pkg/a/x.go", "pkg/b/y.go", "other/src/z.go",
}

// newSparseTestWorktree commits sparseFixtureFiles into a fresh real repo
// and returns the manager plus a leased worktree Create made over it.
func newSparseTestWorktree(t *testing.T) (*WorktreeManager, Worktree) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	for _, f := range sparseFixtureFiles[1:] {
		p := filepath.Join(repo, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(f+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "fixture")
	lease := ResourceLease{RepoID: repo, ScopeGlob: "src/**", Holder: "sparse-1", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	w, err := wm.Create(context.Background(), lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return wm, w
}

// assertOnDisk checks each fixture file's presence matches want.
func assertOnDisk(t *testing.T, root string, want map[string]bool) {
	t.Helper()
	for _, f := range sparseFixtureFiles {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f)))
		if got := err == nil; got != want[f] {
			t.Errorf("%s present = %v, want %v (err=%v)", f, got, want[f], err)
		}
	}
}

func TestWorktreeMinimizeSparseCheckout(t *testing.T) {
	t.Run("keeps only prefixes and deps", sparseKeepsPrefixesAndDeps)
	t.Run("re-minimize widens the set", sparseReMinimizeWidens)
	t.Run("out-of-scope leftover fails closed", sparseLeftoverFailsClosed)
	t.Run("unusable scope refuses before git runs", sparseUnusableScopeRefuses)
	t.Run("git failure is typed", sparseGitFailureTyped)
}

func sparseKeepsPrefixesAndDeps(t *testing.T) {
	ctx := context.Background()
	wm, w := newSparseTestWorktree(t)
	scope := provider.WorktreeScope{InScopePrefixes: []string{"src", "pkg/a/"}, DeclaredDeps: []string{"go.mod"}}
	if err := wm.Minimize(ctx, w, scope); err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	assertOnDisk(t, w.Path, map[string]bool{"go.mod": true, "src/a.go": true, "src/sub/b.go": true, "pkg/a/x.go": true})
	if _, err := os.Stat(filepath.Join(w.Path, "other")); !os.IsNotExist(err) {
		t.Errorf("other/ still on disk: err=%v (prefix must be anchored at the root)", err)
	}
}

func sparseReMinimizeWidens(t *testing.T) {
	ctx := context.Background()
	wm, w := newSparseTestWorktree(t)
	if err := wm.Minimize(ctx, w, provider.WorktreeScope{InScopePrefixes: []string{"src"}}); err != nil {
		t.Fatalf("first Minimize: %v", err)
	}
	assertOnDisk(t, w.Path, map[string]bool{"src/a.go": true, "src/sub/b.go": true})
	if err := wm.Minimize(ctx, w, provider.WorktreeScope{InScopePrefixes: []string{"src", "pkg"}}); err != nil {
		t.Fatalf("second Minimize: %v", err)
	}
	assertOnDisk(t, w.Path, map[string]bool{"src/a.go": true, "src/sub/b.go": true, "pkg/a/x.go": true, "pkg/b/y.go": true})
}

func sparseLeftoverFailsClosed(t *testing.T) {
	ctx := context.Background()
	wm, w := newSparseTestWorktree(t)
	if err := os.WriteFile(filepath.Join(w.Path, "stray.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	err := wm.Minimize(ctx, w, provider.WorktreeScope{InScopePrefixes: []string{"src"}})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Minimize with stray file = %v, want KindIntegrity", err)
	}
}

func sparseUnusableScopeRefuses(t *testing.T) {
	ctx := context.Background()
	wm, w := newSparseTestWorktree(t)
	bad := []provider.WorktreeScope{
		{},
		{InScopePrefixes: []string{""}},
		{InScopePrefixes: []string{"."}},
		{InScopePrefixes: []string{"../x"}},
		{InScopePrefixes: []string{"/etc"}},
		{DeclaredDeps: []string{".git/config"}},
	}
	for _, s := range bad {
		if err := wm.Minimize(ctx, w, s); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Minimize(%+v) = %v, want KindInvalidInput", s, err)
		}
	}
	assertOnDisk(t, w.Path, map[string]bool{
		"README.md": true, "go.mod": true, "src/a.go": true, "src/sub/b.go": true,
		"pkg/a/x.go": true, "pkg/b/y.go": true, "other/src/z.go": true,
	})
	if err := wm.Minimize(ctx, Worktree{}, provider.WorktreeScope{InScopePrefixes: []string{"src"}}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Minimize(empty worktree) = %v, want KindInvalidInput", err)
	}
}

func sparseGitFailureTyped(t *testing.T) {
	ctx := context.Background()
	_, w := newSparseTestWorktree(t)
	wm, _ := newTestWorktreeManager(t)
	broken := wm.withGitBinary(filepath.Join(t.TempDir(), "no-such-git"))
	err := broken.Minimize(ctx, w, provider.WorktreeScope{InScopePrefixes: []string{"src"}})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Minimize with broken git = %v, want KindUnavailable", err)
	}
}
