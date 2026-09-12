package jobs

// Purpose: R-21.147's immutable candidate snapshot against REAL git
//
//	`add --intent-to-add`/`write-tree`: stability across repeated calls,
//	sensitivity to a selected untracked path changing, indifference to an
//	unselected untracked path, and refusal under a stale epoch through
//	the S-59.T2 Fence entry point.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func newSnapshotFixture(t *testing.T) (*WorktreeManager, ResourceLease, Worktree) {
	t.Helper()
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-snap", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	w, err := wm.Create(context.Background(), lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return wm, lease, w
}

// alwaysFencedOK is a FenceFunc that never refuses -- these tests drive
// Snapshot's own git-plumbing behavior, not the fence check itself.
func alwaysFencedOK(context.Context, string, string, int64) error { return nil }

func TestWorktreeSnapshotStableAcrossRepeatedCalls(t *testing.T) {
	wm, lease, w := newSnapshotFixture(t)
	if err := os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	first, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"a.txt"})
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	second, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"a.txt"})
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if first.TreeHash == "" || first.TreeHash != second.TreeHash {
		t.Fatalf("TreeHash unstable across repeated calls on an unchanged index: %q vs %q", first.TreeHash, second.TreeHash)
	}
	if first.Epoch != 1 || second.Epoch != 1 {
		t.Fatalf("Epoch not stamped: first=%d second=%d, want 1", first.Epoch, second.Epoch)
	}
}

func TestWorktreeSnapshotChangesWhenSelectedPathChanges(t *testing.T) {
	wm, lease, w := newSnapshotFixture(t)
	if err := os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	before, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"a.txt"})
	if err != nil {
		t.Fatalf("Snapshot before change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("v2-different-content"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}
	after, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"a.txt"})
	if err != nil {
		t.Fatalf("Snapshot after change: %v", err)
	}
	if before.TreeHash == after.TreeHash {
		t.Fatalf("TreeHash unchanged after a selected untracked path's content changed: %q", before.TreeHash)
	}
}

func TestWorktreeSnapshotIgnoresUnselectedUntrackedPath(t *testing.T) {
	wm, lease, w := newSnapshotFixture(t)
	if err := os.WriteFile(filepath.Join(w.Path, "selected.txt"), []byte("in"), 0o644); err != nil {
		t.Fatalf("write selected.txt: %v", err)
	}
	baseline, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"selected.txt"})
	if err != nil {
		t.Fatalf("baseline Snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "unselected.txt"), []byte("out"), 0o644); err != nil {
		t.Fatalf("write unselected.txt: %v", err)
	}
	after, err := wm.Snapshot(context.Background(), alwaysFencedOK, lease, 1, []string{"selected.txt"})
	if err != nil {
		t.Fatalf("Snapshot with an unselected untracked file present: %v", err)
	}
	if baseline.TreeHash != after.TreeHash {
		t.Fatalf("TreeHash changed after adding an UNSELECTED untracked file: %q -> %q", baseline.TreeHash, after.TreeHash)
	}
}

func TestWorktreeSnapshotRefusedUnderStaleEpoch(t *testing.T) {
	wm, lease, w := newSnapshotFixture(t)
	if err := os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	staleFence := func(_ context.Context, _, _ string, _ int64) error {
		return ErrLeaseFenced
	}
	_, err := wm.Snapshot(context.Background(), staleFence, lease, 99, []string{"a.txt"})
	if err == nil {
		t.Fatal("Snapshot under a stale epoch: want a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("Snapshot stale-epoch error = %v, want a KindConflict cascade.Error", err)
	}
}
