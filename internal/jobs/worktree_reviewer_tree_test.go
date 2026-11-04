package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func reviewerTreeFixture(t *testing.T) (*WorktreeManager, ResourceLease, Worktree) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	store := newTestStore(t)
	lm := NewLeaseManager(store, newTestClock(), func() bool { return true }, DefaultLeaseDefaults(), nil)
	wm := mustNewWorktreeManager(t, store, nil, nil, nil, lm.Fence)
	repo := newTestGitRepo(t)
	result, err := lm.Acquire(context.Background(), repo, "**", "review-job")
	if err != nil || !result.Granted {
		t.Fatalf("Acquire = %+v, %v", result, err)
	}
	w, err := wm.Create(context.Background(), result.Lease, repo)
	if err != nil {
		t.Fatal(err)
	}
	return wm, result.Lease, w
}

func reviewerWrite(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReviewerTreeRefusesDirtyWorktree checks dirtiness without altering the index.
func TestReviewerTreeRefusesDirtyWorktree(t *testing.T) {
	for _, mode := range []string{"unstaged", "staged", "untracked", "gitignored"} {
		t.Run(mode, func(t *testing.T) {
			wm, lease, w := reviewerTreeFixture(t)
			name := "README.md"
			if mode == "untracked" || mode == "gitignored" {
				name = "scratch.txt"
			}
			if mode == "gitignored" {
				reviewerWrite(t, w.Path, ".gitignore", "scratch.txt\n")
				runGitT(t, w.Path, "add", ".gitignore")
				runGitT(t, w.Path, "commit", "-q", "-m", "ignore scratch")
			}
			reviewerWrite(t, w.Path, name, "reviewer change\n")
			if mode == "staged" {
				runGitT(t, w.Path, "add", name)
			}
			before, err := wm.git.run(context.Background(), w.Path, "status", "--porcelain=v1", "--untracked-files=all")
			if err != nil {
				t.Fatal(err)
			}
			tree, err := wm.ReviewerTreeForJob(context.Background(), lease.Holder)
			if mode == "gitignored" {
				if err != nil || tree == "" {
					t.Fatalf("ignored file: tree=%q err=%v", tree, err)
				}
			} else if tree != "" || !errors.Is(err, ErrReviewerMutatedWorktree) || !strings.Contains(err.Error(), ErrReviewerMutatedWorktree.Error()) {
				t.Fatalf("dirty tree=%q err=%v, want reviewer mutation sentinel", tree, err)
			}
			after, err := wm.git.run(context.Background(), w.Path, "status", "--porcelain=v1", "--untracked-files=all")
			if err != nil || before != after {
				t.Fatalf("status changed: %q -> %q, err=%v", before, after, err)
			}
		})
	}
}

// TestReviewerTreeCleanEqualsSnapshot compares the reviewer tree to real Git.
func TestReviewerTreeCleanEqualsSnapshot(t *testing.T) {
	wm, lease, w := reviewerTreeFixture(t)
	ctx := context.Background()
	want, err := wm.git.run(ctx, w.Path, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := wm.Snapshot(ctx, nil, lease, lease.Epoch, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := wm.ReviewerTreeForJob(ctx, lease.Holder)
		if err != nil || got != strings.TrimSpace(want) || got != snapshot.TreeHash {
			t.Fatalf("tree=%q snapshot=%q write-tree=%q err=%v", got, snapshot.TreeHash, want, err)
		}
	}
}

// TestReviewerTreeLeaseErrors checks lease, worktree, fence, and query refusals.
func TestReviewerTreeLeaseErrors(t *testing.T) {
	for _, mode := range []string{"missing", "released", "renewing", "multiple", "pending", "wrong-holder", "query", "fenced", "git"} {
		t.Run(mode, func(t *testing.T) {
			wm, lease, w := reviewerTreeFixture(t)
			ctx := context.Background()
			jobID, want := lease.Holder, cascade.KindNotFound
			switch mode {
			case "missing":
				jobID = "unknown-job"
			case "released", "renewing":
				lease.State = LeaseReleased
				if mode == "renewing" {
					lease.State = LeaseRenewing
				}
				mustPutLease(t, wm.store, lease)
			case "multiple":
				lease.ScopeGlob = "other/**"
				mustPutLease(t, wm.store, lease)
				want = cascade.KindConflict
			case "pending", "wrong-holder":
				if err := deleteWorktreeRow(ctx, wm.store, w.Path); err != nil {
					t.Fatal(err)
				}
				w.Path = jobWorktreeDir(w.Repo, "other-job")
				if mode == "pending" {
					w.Repo = t.TempDir()
					w.Path = jobWorktreeDir(w.Repo, lease.Holder)
				}
				if err := wm.store.PutWorktree(ctx, w); err != nil {
					t.Fatal(err)
				}
			case "query":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = cancelled, cascade.KindUnavailable
			case "fenced":
				wm.fence = func(context.Context, string, string, int64) error { return ErrLeaseFenced }
				want = cascade.KindConflict
			case "git":
				wm = wm.withGitBinary(filepath.Join(t.TempDir(), "missing-git"))
				want = cascade.KindUnavailable
			}
			assertReviewerLeaseResult(ctx, t, wm, jobID, mode, want)
		})
	}
}

func assertReviewerLeaseResult(ctx context.Context, t *testing.T, wm *WorktreeManager, jobID, mode string, want cascade.Kind) {
	t.Helper()
	tree, err := wm.ReviewerTreeForJob(ctx, jobID)
	if mode == "renewing" {
		if tree == "" || err != nil {
			t.Fatalf("renewing: tree=%q err=%v", tree, err)
		}
		return
	}
	if kind, ok := cascade.KindOf(err); tree != "" || !ok || kind != want {
		t.Fatalf("tree=%q err=%v kind=%v, want %v", tree, err, kind, want)
	}
	if mode == "fenced" && (!errors.Is(err, ErrLeaseFenced) || !strings.Contains(err.Error(), ErrLeaseFenced.Error())) {
		t.Fatalf("lost fence sentinel: %v", err)
	}
	if mode == "query" && (!errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), context.Canceled.Error())) {
		t.Fatalf("lost query error: %v", err)
	}
}
