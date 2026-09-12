package jobs

// Purpose: the orphan sweep's eligibility rule (HOW step 4) against a REAL
//
//	git repository: a released+clean+dead-pgid row is removed; a
//	released+dirty+dead-pgid row is quarantined; a live pgid or a
//	non-released lease state blocks the sweep entirely; a stale admin
//	entry (directory removed outside this manager) is pruned; and two
//	consecutive sweeps over an unchanged state return zero deltas.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newSweepFixture creates a real repo, a lease at state, a worktree for
// it, and (optionally) an Execution row recording pgid for the lease's
// holder job.
func newSweepFixture(t *testing.T, wm *WorktreeManager, store *Store, state LeaseState, holder string, pgid int64) (repo string, lease ResourceLease, w Worktree) {
	t.Helper()
	ctx := context.Background()
	repo = newTestGitRepo(t)
	lease = ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: holder, Epoch: 1, State: state}
	mustPutLease(t, store, lease)
	if pgid != 0 {
		if err := store.PutJob(ctx, baseJob(holder)); err != nil {
			t.Fatalf("PutJob: %v", err)
		}
		if err := store.PutExecution(ctx, Execution{ID: holder + "-e1", JobID: holder, Attempt: 1, State: ExecutionRunning, PGID: pgid}); err != nil {
			t.Fatalf("PutExecution: %v", err)
		}
	}
	w, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create fixture: %v", err)
	}
	return repo, lease, w
}

func TestWorktreeSweepRemovesReleasedCleanDeadPGIDOrphan(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo, _, w := newSweepFixture(t, wm, store, LeaseReleased, "job-clean", 0)

	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != w.Path {
		t.Fatalf("Removed = %v, want [%q]", result.Removed, w.Path)
	}
	if len(result.Quarantined) != 0 {
		t.Fatalf("Quarantined = %v, want none", result.Quarantined)
	}
	if len(result.Pruned) != 1 || result.Pruned[0] != repo {
		t.Fatalf("Pruned = %v, want [%q]", result.Pruned, repo)
	}
	if _, statErr := os.Stat(w.Path); !os.IsNotExist(statErr) {
		t.Fatalf("worktree dir still present: %v", statErr)
	}

	second, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(second.Removed) != 0 || len(second.Quarantined) != 0 || len(second.Pruned) != 0 {
		t.Fatalf("second Sweep = %+v, want zero deltas (idempotent)", second)
	}
}

func TestWorktreeSweepQuarantinesReleasedDirtyDeadPGIDOrphan(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	_, lease, w := newSweepFixture(t, wm, store, LeaseReleased, "job-dirty", 0)
	if err := os.WriteFile(filepath.Join(w.Path, "untracked.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("dirty file: %v", err)
	}

	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("Removed = %v, want none (dirty orphan quarantines, never removes)", result.Removed)
	}
	if len(result.Quarantined) != 1 || result.Quarantined[0] != w.Path {
		t.Fatalf("Quarantined = %v, want [%q]", result.Quarantined, w.Path)
	}
	wantQ := quarantinePath(w.Repo, lease.Holder)
	if _, statErr := os.Stat(wantQ); statErr != nil {
		t.Fatalf("quarantine dir missing at %q: %v", wantQ, statErr)
	}
	if _, statErr := os.Stat(w.Path); !os.IsNotExist(statErr) {
		t.Fatalf("original path still present after quarantine move: %v", statErr)
	}
	if _, ok, err := worktreeRowForLease(context.Background(), store, lease.RepoID, lease.ScopeGlob); err != nil || !ok {
		t.Fatalf("row missing after quarantine: ok=%v err=%v", ok, err)
	}

	second, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(second.Removed) != 0 || len(second.Quarantined) != 0 {
		t.Fatalf("second Sweep over an already-quarantined row = %+v, want zero deltas", second)
	}
}

func TestWorktreeSweepLeavesLivePGIDUntouched(t *testing.T) {
	store := newTestStore(t)
	wm := NewWorktreeManager(store, nil, nil, fakeLivenessProbe{alive: true})
	_, lease, w := newSweepFixture(t, wm, store, LeaseReleased, "job-live", 4242)
	if err := os.WriteFile(filepath.Join(w.Path, "untracked.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("dirty file: %v", err)
	}

	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Quarantined) != 0 {
		t.Fatalf("Sweep with a live pgid = %+v, want zero deltas (blocked entirely)", result)
	}
	if _, statErr := os.Stat(w.Path); statErr != nil {
		t.Fatalf("worktree with a live pgid was touched: %v", statErr)
	}
	if _, ok, err := worktreeRowForLease(context.Background(), store, lease.RepoID, lease.ScopeGlob); err != nil || !ok {
		t.Fatalf("row for a live-pgid worktree disappeared: ok=%v err=%v", ok, err)
	}
}

func TestWorktreeSweepLeavesExpiredUnconfirmedUntouched(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	_, lease, w := newSweepFixture(t, wm, store, LeaseExpiredUnconfirmed, "job-unconfirmed", 0)

	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Removed) != 0 || len(result.Quarantined) != 0 {
		t.Fatalf("Sweep over an expired_unconfirmed lease = %+v, want zero deltas", result)
	}
	if _, statErr := os.Stat(w.Path); statErr != nil {
		t.Fatalf("expired_unconfirmed worktree was touched: %v", statErr)
	}
	if _, ok, err := worktreeRowForLease(context.Background(), store, lease.RepoID, lease.ScopeGlob); err != nil || !ok {
		t.Fatalf("row for an expired_unconfirmed worktree disappeared: ok=%v err=%v", ok, err)
	}
}

// TestWorktreeSweepStaleAdminEntryPruned simulates a worktree directory removed
// OUTSIDE this manager (e.g. `rm -rf` by hand): the stored row and git's
// own admin metadata both go stale. Sweep must clear the row and run
// `git worktree prune` so git's admin metadata catches up.
func TestWorktreeSweepStaleAdminEntryPruned(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo, lease, w := newSweepFixture(t, wm, store, LeaseReleased, "job-stale", 0)
	if err := os.RemoveAll(w.Path); err != nil {
		t.Fatalf("simulate external removal: %v", err)
	}

	entriesBefore, err := wm.listGitWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("listGitWorktrees before sweep: %v", err)
	}
	if len(entriesBefore) != 2 {
		t.Fatalf("git admin entries before sweep = %d, want 2 (stale entry still registered)", len(entriesBefore))
	}

	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != w.Path {
		t.Fatalf("Removed = %v, want [%q]", result.Removed, w.Path)
	}
	if len(result.Pruned) != 1 || result.Pruned[0] != repo {
		t.Fatalf("Pruned = %v, want [%q]", result.Pruned, repo)
	}
	entriesAfter, err := wm.listGitWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatalf("listGitWorktrees after sweep: %v", err)
	}
	if len(entriesAfter) != 1 {
		t.Fatalf("git admin entries after sweep+prune = %d, want 1 (stale entry pruned)", len(entriesAfter))
	}
	if _, ok, err := worktreeRowForLease(context.Background(), store, lease.RepoID, lease.ScopeGlob); err != nil || ok {
		t.Fatalf("stale row still present: ok=%v err=%v", ok, err)
	}
}
