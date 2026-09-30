package jobs

// Purpose: AUD-004 fencing of WorktreeManager.Create against a REAL git
//
//	repository and the REAL (*LeaseManager).Fence: every weak-lease input
//	(released, expired_orphaned, stale epoch, no lease, and the two
//	existing-row variants) is refused with ErrLeaseFenced before any git
//	command runs; a new holder never inherits a previous holder's tree;
//	a fence lost during the add is compensated back to the pre-Create
//	git and store state; and the constructor refuses a nil fence.
//
// SPORT: jobs/worktree-manager (P1-CORE-06).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mustNewWorktreeManager constructs a WorktreeManager, failing the test on
// a constructor error.
func mustNewWorktreeManager(t *testing.T, store *Store, j journal.Store, attn *supervision.Store, probe ProcessLivenessProbe, fence FenceFunc) *WorktreeManager {
	t.Helper()
	wm, err := NewWorktreeManager(store, j, attn, probe, fence)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}
	return wm
}

// realLeaseFence returns the production fence, (*LeaseManager).Fence.
func realLeaseFence(store *Store) FenceFunc {
	return NewLeaseManager(store, newTestClock(), func() bool { return true }, DefaultLeaseDefaults(), nil).Fence
}

// requireLeaseFenced asserts err carries ErrLeaseFenced by pointer identity
// in its chain AND by message (errors.Is on cascade errors compares Kind only).
func requireLeaseFenced(t *testing.T, err error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(ErrLeaseFenced) {
			if !strings.Contains(err.Error(), ErrLeaseFenced.Msg) {
				t.Fatalf("error %q lacks ErrLeaseFenced's message %q", err, ErrLeaseFenced.Msg)
			}
			return
		}
	}
	t.Fatalf("error = %v, want ErrLeaseFenced in its chain", err)
}

// gitOut runs the real git in dir and returns its stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// gitState is the observable git state a refused Create must not change.
func gitState(t *testing.T, repo string) string {
	return gitOut(t, repo, "worktree", "list", "--porcelain") + "\x00" + gitOut(t, repo, "branch", "--list", "job/*")
}

func TestNewWorktreeManagerRequiresFence(t *testing.T) {
	wm, err := NewWorktreeManager(newTestStore(t), nil, nil, fakeLivenessProbe{}, nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("NewWorktreeManager(nil fence) err = %v, want KindInvalidInput", err)
	}
	if wm != nil {
		t.Fatal("NewWorktreeManager(nil fence) returned a manager")
	}
}

// fencedCase seeds one weak-lease state and returns the lease Create is
// then called with.
type fencedCase struct {
	name string
	seed func(t *testing.T, wm *WorktreeManager, store *Store, repo string) ResourceLease
}

func fencedCases() []fencedCase {
	put := func(t *testing.T, store *Store, repo string, state LeaseState, epoch int64) ResourceLease {
		l := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-f", Epoch: epoch, State: state}
		mustPutLease(t, store, l)
		return l
	}
	withRow := func(t *testing.T, wm *WorktreeManager, store *Store, repo string) ResourceLease {
		l := put(t, store, repo, LeaseHeld, 1)
		if _, err := wm.Create(context.Background(), l, repo); err != nil {
			t.Fatalf("seed Create: %v", err)
		}
		return l
	}
	return []fencedCase{
		{"released", func(t *testing.T, _ *WorktreeManager, s *Store, r string) ResourceLease {
			return put(t, s, r, LeaseReleased, 1)
		}},
		{"expired_orphaned", func(t *testing.T, _ *WorktreeManager, s *Store, r string) ResourceLease {
			return put(t, s, r, LeaseExpiredOrphaned, 2)
		}},
		{"stale_epoch", func(t *testing.T, _ *WorktreeManager, s *Store, r string) ResourceLease {
			l := put(t, s, r, LeaseHeld, 3)
			l.Epoch = 2
			return l
		}},
		{"no_lease_row", func(_ *testing.T, _ *WorktreeManager, _ *Store, r string) ResourceLease {
			return ResourceLease{RepoID: r, ScopeGlob: "**", Holder: "job-f", Epoch: 1, State: LeaseHeld}
		}},
		{"existing_row_released", func(t *testing.T, wm *WorktreeManager, s *Store, r string) ResourceLease {
			l := withRow(t, wm, s, r)
			return put(t, s, r, LeaseReleased, l.Epoch)
		}},
		{"existing_row_stale_epoch", func(t *testing.T, wm *WorktreeManager, s *Store, r string) ResourceLease {
			l := withRow(t, wm, s, r)
			put(t, s, r, LeaseHeld, 2)
			return l
		}},
	}
}

func TestWorktreeCreateRefusesFencedLease(t *testing.T) {
	for _, tc := range fencedCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
			repo := newTestGitRepo(t)
			ctx := context.Background()
			lease := tc.seed(t, wm, store, repo)
			rowBefore, hadRow, _ := worktreeRowForLease(ctx, store, repo, "**")
			bin, record := recordingGit(t)
			before := gitState(t, repo)

			w, err := wm.withGitBinary(bin).Create(ctx, lease, repo)
			requireLeaseFenced(t, err)
			if w != (Worktree{}) {
				t.Fatalf("fenced Create returned a worktree: %+v", w)
			}
			requireNoGitCalls(t, record, "fenced Create")
			if after := gitState(t, repo); after != before {
				t.Fatalf("git state changed:\nbefore %q\nafter  %q", before, after)
			}
			rowAfter, hasRow, err := worktreeRowForLease(ctx, store, repo, "**")
			if err != nil || hasRow != hadRow || rowAfter != rowBefore {
				t.Fatalf("worktree row changed: before (%v %+v) after (%v %+v) err=%v", hadRow, rowBefore, hasRow, rowAfter, err)
			}
		})
	}
}

func TestWorktreeCreateNewHolderDoesNotInheritRow(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			store := newTestStore(t)
			wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
			repo := newTestGitRepo(t)
			ctx := context.Background()
			leaseA := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "a", Epoch: 1, State: LeaseHeld}
			mustPutLease(t, store, leaseA)
			wA, err := wm.Create(ctx, leaseA, repo)
			if err != nil {
				t.Fatalf("Create A: %v", err)
			}
			if dirty {
				if err := os.WriteFile(filepath.Join(wA.Path, "work.txt"), []byte("a's work"), 0o644); err != nil {
					t.Fatalf("dirty A: %v", err)
				}
			}
			leaseB := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "b", Epoch: 2, State: LeaseHeld}
			mustPutLease(t, store, leaseB)

			w, err := wm.Create(ctx, leaseB, repo)
			if w.Path == wA.Path {
				t.Fatalf("Create for holder b returned holder a's worktree %q", wA.Path)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
				t.Fatalf("Create for b over a's live row: err = %v, want a typed KindConflict", err)
			}
			assertPreviousHolderSwept(t, wm, repo, wA, dirty)
			wB, err := wm.Create(ctx, leaseB, repo)
			if err != nil || wB.Path != jobWorktreeDir(repo, "b") {
				t.Fatalf("Create b after sweep = %+v, %v; want b's own path", wB, err)
			}
		})
	}
}

// assertPreviousHolderSwept runs Sweep and checks holder a's tree was
// removed (clean) or quarantined with its work intact (dirty).
func assertPreviousHolderSwept(t *testing.T, wm *WorktreeManager, repo string, wA Worktree, dirty bool) {
	t.Helper()
	result, err := wm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, statErr := os.Stat(wA.Path); !os.IsNotExist(statErr) {
		t.Fatalf("a's worktree still at %q after Sweep (err=%v)", wA.Path, statErr)
	}
	if !dirty {
		if len(result.Removed) != 1 || result.Removed[0] != wA.Path {
			t.Fatalf("Sweep Removed = %v, want [%q]", result.Removed, wA.Path)
		}
		return
	}
	if len(result.Quarantined) != 1 || result.Quarantined[0] != wA.Path {
		t.Fatalf("Sweep Quarantined = %v, want [%q]", result.Quarantined, wA.Path)
	}
	got, err := os.ReadFile(filepath.Join(quarantinePath(repo, "a"), "work.txt"))
	if err != nil || string(got) != "a's work" {
		t.Fatalf("a's dirty work lost: %q, %v", got, err)
	}
}

// TestWorktreeCreatePostAddFenceCompensates releases the lease while the
// add runs: the fence passes before the add and refuses after it, so
// Create must undo the add, its branch, and its intent row.
func TestWorktreeCreatePostAddFenceCompensates(t *testing.T) {
	store := newTestStore(t)
	leaseFence := realLeaseFence(store)
	calls := 0
	lease := ResourceLease{RepoID: "", ScopeGlob: "**", Holder: "job-late", Epoch: 1, State: LeaseHeld}
	releasingFence := func(ctx context.Context, repoID, scope string, epoch int64) error {
		calls++
		err := leaseFence(ctx, repoID, scope, epoch)
		if calls == 1 {
			released := lease
			released.State = LeaseReleased
			mustPutLease(t, store, released)
		}
		return err
	}
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, releasingFence)
	repo := newTestGitRepo(t)
	lease.RepoID = repo
	mustPutLease(t, store, lease)
	before := gitState(t, repo)

	w, err := wm.Create(context.Background(), lease, repo)
	requireLeaseFenced(t, err)
	if calls != 2 || w != (Worktree{}) {
		t.Fatalf("fence calls = %d, worktree = %+v; want 2 calls and no worktree", calls, w)
	}
	if after := gitState(t, repo); after != before {
		t.Fatalf("git state not restored:\nbefore %q\nafter  %q", before, after)
	}
	path := jobWorktreeDir(repo, lease.Holder)
	if _, ok, err := store.GetWorktree(context.Background(), path); err != nil || ok {
		t.Fatalf("intent row survived compensation: ok=%v err=%v", ok, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("worktree directory survived compensation: %v", statErr)
	}
}

// TestWorktreeCreateRefusesForgedHolder: the current epoch with a holder
// that is not the stored lease holder is refused before any git command.
func TestWorktreeCreateRefusesForgedHolder(t *testing.T) {
	store := newTestStore(t)
	wm := mustNewWorktreeManager(t, store, nil, nil, fakeLivenessProbe{}, realLeaseFence(store))
	repo := newTestGitRepo(t)
	ctx := context.Background()
	mustPutLease(t, store, ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "real", Epoch: 5, State: LeaseHeld})
	forged := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "forged", Epoch: 5, State: LeaseHeld}
	bin, record := recordingGit(t)
	before := gitState(t, repo)

	w, err := wm.withGitBinary(bin).Create(ctx, forged, repo)
	requireLeaseFenced(t, err)
	if w != (Worktree{}) {
		t.Fatalf("forged-holder Create returned a worktree: %+v", w)
	}
	requireNoGitCalls(t, record, "forged-holder Create")
	if after := gitState(t, repo); after != before {
		t.Fatalf("git state changed:\nbefore %q\nafter  %q", before, after)
	}
	if _, ok, err := worktreeRowForLease(ctx, store, repo, "**"); err != nil || ok {
		t.Fatalf("forged-holder Create left a row: ok=%v err=%v", ok, err)
	}
}
