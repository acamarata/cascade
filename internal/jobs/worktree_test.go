package jobs

// Purpose: Create/Remove round-trip, converge re-create, the injected-
//
//	binary-path error seam, a dirty-remove refusal, and the R-21.177
//	per-repository mutex under real concurrency (-race) — all against a
//	REAL git repository under t.TempDir() (Art.2).
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// runGitT runs git args in dir, failing the test on any non-zero exit.
func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// newTestGitRepo initializes a real, minimally-committed git repository
// under t.TempDir() — Art.2's real counterpart, never a self-authored
// double of git's own behavior.
func newTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// macOS's t.TempDir() lives under /var/folders, a symlink to
	// /private/var/folders; git worktree list --porcelain always reports
	// the resolved real path. Resolving here once keeps every path this
	// test suite compares against git's own output byte-identical,
	// instead of every test re-deriving the same normalization.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	runGitT(t, dir, "init", "-q")
	runGitT(t, dir, "config", "user.email", "worktree-test@example.invalid")
	runGitT(t, dir, "config", "user.name", "worktree-test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runGitT(t, dir, "add", "README.md")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// newTestWorktreeManager returns a WorktreeManager over a fresh real
// store, with no journal/attention wiring (worktree_events_test.go and
// worktree_quarantine_test.go cover those integrations).
func newTestWorktreeManager(t *testing.T) (*WorktreeManager, *Store) {
	t.Helper()
	store := newTestStore(t)
	return NewWorktreeManager(store, nil, nil, fakeLivenessProbe{alive: false}), store
}

func mustPutLease(t *testing.T, store *Store, l ResourceLease) {
	t.Helper()
	if err := store.PutLease(context.Background(), l); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
}

func TestWorktreeCreateRemoveRoundTrip(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "internal/**", Holder: "job-1", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	w, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantPath := filepath.Join(repo, ".cascade", "worktrees", "job-job-1")
	if w.Path != wantPath {
		t.Fatalf("Path = %q, want %q", w.Path, wantPath)
	}
	if w.Branch != "job/job-1" {
		t.Fatalf("Branch = %q, want job/job-1", w.Branch)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatalf("worktree dir missing on disk: %v", err)
	}
	row, ok, err := worktreeRowForLease(ctx, store, lease.RepoID, lease.ScopeGlob)
	if err != nil || !ok {
		t.Fatalf("worktreeRowForLease: ok=%v err=%v", ok, err)
	}
	if row.Path != wantPath {
		t.Fatalf("persisted row Path = %q, want %q", row.Path, wantPath)
	}

	if err := wm.Remove(ctx, lease); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present after Remove: err=%v", err)
	}
	if _, ok, err := worktreeRowForLease(ctx, store, lease.RepoID, lease.ScopeGlob); err != nil || ok {
		t.Fatalf("row still present after Remove: ok=%v err=%v", ok, err)
	}
}

// TestWorktreeCreateConverges proves a second Create for the SAME lease
// does not re-run `git worktree add` (which would itself fail on an
// already-existing path/branch) and returns the same row.
func TestWorktreeCreateConverges(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "internal/**", Holder: "job-1", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	first, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("second (converging) Create: %v", err)
	}
	if second != first {
		t.Fatalf("converged Create = %+v, want identical %+v", second, first)
	}

	out, err := wm.git.run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	entries, err := ParseWorktreePorcelain([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	count := 0
	for _, e := range entries {
		if e.Path == first.Path {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("git worktree list shows %d entries for %q, want exactly 1 (no second add)", count, first.Path)
	}
}

// TestWorktreeCreateNonRepoRootIsTypedError proves a non-repo root fails
// with a typed A-T7 error, never a panic or a bare exec error.
func TestWorktreeCreateNonRepoRootIsTypedError(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	notARepo := t.TempDir()
	ctx := context.Background()
	lease := ResourceLease{RepoID: notARepo, ScopeGlob: "**", Holder: "job-x", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	_, err := wm.Create(ctx, lease, notARepo)
	if err == nil {
		t.Fatal("Create over a non-repo root: want error, got nil")
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatalf("error is not a typed cascade.Error: %v", err)
	}
}

// TestWorktreeCreateBrokenGitBinaryIsTypedError exercises the injected
// binary-path seam: pointing at a nonexistent executable is a git
// BINARY FAILURE, distinct from a git-command-refused failure.
func TestWorktreeCreateBrokenGitBinaryIsTypedError(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	broken := wm.withGitBinary(filepath.Join(t.TempDir(), "no-such-git-binary"))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-y", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	_, err := broken.Create(ctx, lease, repo)
	if err == nil {
		t.Fatal("Create with a broken git binary: want error, got nil")
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatalf("error is not a typed cascade.Error: %v", err)
	}
}

// TestWorktreeRemoveDirtyRefuses proves Remove never silently force-
// deletes a dirty per-lease worktree: it fails naming the path, and the
// directory is untouched.
func TestWorktreeRemoveDirtyRefuses(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-dirty", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)

	w, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "untracked.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("dirty file: %v", err)
	}

	err = wm.Remove(ctx, lease)
	if err == nil {
		t.Fatal("Remove over a dirty worktree: want error, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindConflict {
		t.Fatalf("Remove error = %v (kind=%v ok=%v), want a KindConflict cascade.Error", err, kind, ok)
	}
	if _, statErr := os.Stat(w.Path); statErr != nil {
		t.Fatalf("dirty worktree was removed from disk: %v", statErr)
	}
}

// TestWorktreeRepoMutex drives N concurrent Create calls against ONE
// repository under -race, proving the per-repository mutex + backoff
// (HOW step 10) lets every one land with no index.lock error.
func TestWorktreeRepoMutex(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	ctx := context.Background()
	const n = 12

	var start sync.WaitGroup
	start.Add(1)
	var ready, done sync.WaitGroup
	ready.Add(n)
	done.Add(n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		i := i
		lease := ResourceLease{RepoID: repo, ScopeGlob: fmt.Sprintf("scope-%d/**", i), Holder: fmt.Sprintf("job-%d", i), Epoch: 1, State: LeaseHeld}
		mustPutLease(t, store, lease)
		go func() {
			defer done.Done()
			ready.Done()
			start.Wait()
			_, err := wm.Create(ctx, lease, repo)
			errs[i] = err
		}()
	}
	ready.Wait()
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Create[%d]: %v", i, err)
		}
		path := filepath.Join(repo, ".cascade", "worktrees", fmt.Sprintf("job-job-%d", i))
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("worktree %d missing on disk: %v", i, statErr)
		}
	}
}
