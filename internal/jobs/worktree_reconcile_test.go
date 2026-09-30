package jobs

// Purpose: Sweep's reconciliation of `git worktree list` against stored
//
//	rows, against a REAL git repository: an unrecorded clean job worktree
//	is removed with its commit-free branch, an unrecorded clean worktree
//	whose branch carries its own commit is removed while the branch
//	survives, an unrecorded dirty one is quarantined with its work intact,
//	a row whose path is missing under a dead lease is deleted, and the
//	live holder's recorded worktree is untouched.
//
// SPORT: jobs/worktree-manager (P1-CORE-06).

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// reconcileFixture holds the repo and the anchor worktree (the live
// holder's own recorded tree, which also makes the repo known to Sweep).
type reconcileFixture struct {
	wm     *WorktreeManager
	store  *Store
	repo   string
	anchor Worktree
}

func newReconcileFixture(t *testing.T) reconcileFixture {
	t.Helper()
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	anchorLease := ResourceLease{RepoID: repo, ScopeGlob: "anchor/**", Holder: "anchor", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, anchorLease)
	anchor, err := wm.Create(ctx, anchorLease, repo)
	if err != nil {
		t.Fatalf("Create anchor: %v", err)
	}
	// Unrecorded job worktrees, made by git directly (no row).
	for _, id := range []string{"clean", "kept", "dirty"} {
		runGitT(t, repo, "worktree", "add", jobWorktreeDir(repo, id), "-b", jobBranch(id))
	}
	runGitT(t, jobWorktreeDir(repo, "kept"), "commit", "-q", "--allow-empty", "-m", "work only on job/kept")
	if err := os.WriteFile(filepath.Join(jobWorktreeDir(repo, "dirty"), "work.txt"), []byte("unsaved"), 0o644); err != nil {
		t.Fatalf("dirty file: %v", err)
	}
	// A row whose tree never appeared, under a released lease.
	gone := ResourceLease{RepoID: repo, ScopeGlob: "gone/**", Holder: "gone", Epoch: 1, State: LeaseReleased}
	mustPutLease(t, store, gone)
	if err := store.PutWorktree(ctx, Worktree{Path: jobWorktreeDir(repo, "gone"), LeaseRepoID: repo, LeaseScopeGlob: "gone/**", Repo: repo, Branch: jobBranch("gone")}); err != nil {
		t.Fatalf("PutWorktree gone: %v", err)
	}
	return reconcileFixture{wm: wm, store: store, repo: repo, anchor: anchor}
}

func TestWorktreeSweepReconcilesGitList(t *testing.T) {
	f := newReconcileFixture(t)
	ctx := context.Background()
	result, err := f.wm.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	wantRemoved := []string{jobWorktreeDir(f.repo, "clean"), jobWorktreeDir(f.repo, "gone"), jobWorktreeDir(f.repo, "kept")}
	sort.Strings(result.Removed)
	if strings.Join(result.Removed, "|") != strings.Join(wantRemoved, "|") {
		t.Fatalf("Removed = %v, want %v", result.Removed, wantRemoved)
	}
	if len(result.Quarantined) != 1 || result.Quarantined[0] != jobWorktreeDir(f.repo, "dirty") {
		t.Fatalf("Quarantined = %v, want the dirty unrecorded worktree", result.Quarantined)
	}
	if got, err := os.ReadFile(filepath.Join(quarantinePath(f.repo, "dirty"), "work.txt")); err != nil || string(got) != "unsaved" {
		t.Fatalf("dirty work lost in quarantine: %q, %v", got, err)
	}
	if _, ok, err := f.store.GetWorktree(ctx, jobWorktreeDir(f.repo, "gone")); err != nil || ok {
		t.Fatalf("row with a missing path survived: ok=%v err=%v", ok, err)
	}
	branches := gitOut(t, f.repo, "branch", "--list", "job/*", "--format=%(refname:short)")
	if strings.Contains(branches, "job/clean\n") || !strings.Contains(branches, "job/kept\n") || !strings.Contains(branches, "job/anchor\n") {
		t.Fatalf("branches after Sweep = %q; want job/clean deleted, job/kept and job/anchor kept", branches)
	}
	listed, rows := jobWorktreeSets(t, f.wm, f.store, f.repo)
	if len(listed) != 1 || len(rows) != 1 || listed[0] != canonicalPath(f.anchor.Path) || rows[0] != listed[0] {
		t.Fatalf("after Sweep git job worktrees %v, rows %v; want only the anchor %q", listed, rows, f.anchor.Path)
	}
	second, err := f.wm.Sweep(ctx)
	if err != nil || len(second.Removed)+len(second.Quarantined)+len(second.Pruned) != 0 {
		t.Fatalf("second Sweep = %+v, %v; want zero deltas", second, err)
	}
}

// requireReachable asserts sha is still reachable from some ref or
// worktree HEAD (`rev-list --all` walks every worktree's HEAD).
func requireReachable(t *testing.T, repo, sha, what string) {
	t.Helper()
	if !strings.Contains(gitOut(t, repo, "rev-list", "--all"), sha+"\n") {
		t.Fatalf("%s: commit %s is no longer reachable", what, sha)
	}
}

// TestWorktreeSweepKeepsDetachedUnreachableHead: an unrecorded clean job
// worktree on a DETACHED HEAD carrying a commit no ref contains survives
// Sweep with its commit reachable; a detached tree whose HEAD a branch
// already contains is still removed.
func TestWorktreeSweepKeepsDetachedUnreachableHead(t *testing.T) {
	f := newReconcileFixture(t)
	ctx := context.Background()
	det, detOK := jobWorktreeDir(f.repo, "det"), jobWorktreeDir(f.repo, "detok")
	runGitT(t, f.repo, "worktree", "add", "--detach", det)
	runGitT(t, f.repo, "worktree", "add", "--detach", detOK)
	runGitT(t, det, "commit", "-q", "--allow-empty", "-m", "detached work")
	sha := strings.TrimSpace(gitOut(t, det, "rev-parse", "HEAD"))
	if refs := strings.TrimSpace(gitOut(t, f.repo, "for-each-ref", "--contains", sha)); refs != "" {
		t.Fatalf("fixture: detached commit already on a ref: %q", refs)
	}
	result, err := f.wm.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, p := range append(append([]string{}, result.Removed...), result.Quarantined...) {
		if p == det {
			t.Fatalf("Sweep removed or quarantined the detached tree carrying %s: %+v", sha, result)
		}
	}
	if _, statErr := os.Stat(det); statErr != nil {
		t.Fatalf("detached tree gone after Sweep: %v", statErr)
	}
	if !strings.Contains(gitOut(t, f.repo, "worktree", "list", "--porcelain"), "HEAD "+sha+"\n") {
		t.Fatal("detached tree no longer listed by git worktree list")
	}
	requireReachable(t, f.repo, sha, "after Sweep")
	if _, statErr := os.Stat(detOK); !os.IsNotExist(statErr) {
		t.Fatalf("detached tree whose HEAD a branch contains survived Sweep: %v", statErr)
	}
}

// TestWorktreeSweepKeepsDetachedRowTree: a recorded tree under a released
// lease whose HEAD was detached onto a commit no ref contains is neither
// removed nor quarantined; its row stays and its commit stays reachable.
func TestWorktreeSweepKeepsDetachedRowTree(t *testing.T) {
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "x", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	w, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runGitT(t, w.Path, "checkout", "-q", "--detach")
	runGitT(t, w.Path, "commit", "-q", "--allow-empty", "-m", "detached work in a recorded tree")
	sha := strings.TrimSpace(gitOut(t, w.Path, "rev-parse", "HEAD"))
	lease.State = LeaseReleased
	mustPutLease(t, store, lease)
	result, err := wm.Sweep(ctx)
	if err != nil || len(result.Removed)+len(result.Quarantined) != 0 {
		t.Fatalf("Sweep = %+v, %v; want the detached recorded tree untouched", result, err)
	}
	if _, ok, err := store.GetWorktree(ctx, w.Path); err != nil || !ok {
		t.Fatalf("row of the detached recorded tree gone: ok=%v err=%v", ok, err)
	}
	if _, statErr := os.Stat(w.Path); statErr != nil {
		t.Fatalf("detached recorded tree gone: %v", statErr)
	}
	requireReachable(t, repo, sha, "after Sweep")
}

// TestWorktreeSweepKeepsLivePendingRow: the live holder's pending intent
// row (written, add not yet run) survives Sweep so Create can retry.
func TestWorktreeSweepKeepsLivePendingRow(t *testing.T) {
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "live", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	pending := Worktree{Path: jobWorktreeDir(repo, "live"), LeaseRepoID: repo, LeaseScopeGlob: "**", Repo: repo, Branch: jobBranch("live")}
	if err := store.PutWorktree(ctx, pending); err != nil {
		t.Fatalf("PutWorktree (intent row): %v", err)
	}
	result, err := wm.Sweep(ctx)
	if err != nil || len(result.Removed) != 0 {
		t.Fatalf("Sweep = %+v, %v; want the live pending row kept", result, err)
	}
	if row, ok, err := store.GetWorktree(ctx, pending.Path); err != nil || !ok || row != pending {
		t.Fatalf("live pending row after Sweep = %+v ok=%v err=%v, want %+v", row, ok, err, pending)
	}
	if w, err := wm.Create(ctx, lease, repo); err != nil || w != pending {
		t.Fatalf("Create after Sweep = %+v, %v; want the retried add at %q", w, err, pending.Path)
	}
}
