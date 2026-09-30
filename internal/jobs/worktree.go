package jobs

// Purpose: WorktreeManager (HOW steps 1-3): one isolated git worktree per
//
//	held lease at the R-16.37 verbatim constants, driven by the REAL git
//	binary over os/exec (no CGO). Create/Remove are the two mutating
//	entry points; worktree_mutex.go owns the per-repo admin-mutation
//	serialization (HOW step 10), worktree_store.go owns this ticket's
//	direct-SQL row helpers, and worktree_events.go wires Create/Remove to
//	the S-59.T2 lease lifecycle (same package as lease_events.go).
//
// Inputs: a ResourceLease plus (Create only) the repo root a
//
//	RepoRootResolver (worktree_events.go) produced; every other caller
//	reads Worktree.Repo (the root, recorded at Create time) off the row.
//
// Outputs: a persisted Worktree row and a real checked-out working tree,
//
//	or a typed A-T7 error wrapping git's own stderr.
//
// Constraints: no CGO (os/exec only); a dirty Remove() on the per-lease
//
//	path never force-deletes — only the sweep's quarantine path
//	(worktree_quarantine.go) uses --force, and only after moving the
//	tree off its original path.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
)

// worktreesDirName is the R-16.37 verbatim path segment. jobWorktreeDir/
// jobBranch apply the id -> path/branch mapping every caller uses.
const worktreesDirName = ".cascade/worktrees"

func jobWorktreeDir(repoRoot, jobID string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(worktreesDirName), "job-"+jobID)
}

func jobBranch(jobID string) string {
	return "job/" + jobID
}

// gitRunner is the injected exec seam (Art.2): production always shells to
// a real git binary and every test drives that same real binary against a
// throwaway t.TempDir() repository. Only the BINARY PATH is injectable —
// error-path tests point it at a broken stand-in executable rather than
// replacing git's own semantics with a self-authored double.
type gitRunner interface {
	run(ctx context.Context, dir string, args ...string) (stdout string, err error)
}

type execGitRunner struct{ bin string }

func newExecGitRunner(bin string) execGitRunner {
	if bin == "" {
		bin = "git"
	}
	return execGitRunner{bin: bin}
}

func (r execGitRunner) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), cascade.Wrapf(cascade.KindUnavailable, err,
			"jobs: git %s (in %s) failed: %s", strings.Join(args, " "), dir, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// WorktreeManager is the per-lease worktree authority (HOW steps 1-3).
// The zero value is not usable; construct with NewWorktreeManager.
type WorktreeManager struct {
	store     *Store
	git       gitRunner
	sleep     sleeper
	mutexes   *repoMutexRegistry
	journal   journal.Store        // may be nil: no journal integration (unit tests)
	attention *supervision.Store   // may be nil: no attention-queue integration (unit tests)
	probe     ProcessLivenessProbe // never nil; defaults to the real per-platform probe
	fence     FenceFunc            // never nil: every Create is fenced (C16)
}

// NewWorktreeManager constructs a WorktreeManager over store, shelling out
// to the git binary found on PATH. j and attn may be nil to disable their
// respective integrations (unit tests). A nil probe defaults to
// NewProcessLivenessProbe() (lease_fence_unix.go/lease_fence_windows.go's
// real per-platform signal/handle check) — pass a fake probe in sweep
// tests for deterministic pgid-liveness outcomes. fence is REQUIRED
// (production passes (*LeaseManager).Fence): a nil fence refuses with
// KindInvalidInput and no manager, so no composition root can build an
// unfenced Create.
func NewWorktreeManager(store *Store, j journal.Store, attn *supervision.Store, probe ProcessLivenessProbe, fence FenceFunc) (*WorktreeManager, error) {
	if fence == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: worktree manager requires a lease fence")
	}
	if probe == nil {
		probe = NewProcessLivenessProbe()
	}
	return &WorktreeManager{
		store:     store,
		git:       newExecGitRunner(""),
		sleep:     realSleeper{},
		mutexes:   newRepoMutexRegistry(),
		journal:   j,
		attention: attn,
		probe:     probe,
		fence:     fence,
	}, nil
}

// withGitBinary returns a copy of m shelling out to bin instead of the
// PATH-resolved git — the injected-binary-path seam error-path tests use.
func (m *WorktreeManager) withGitBinary(bin string) *WorktreeManager {
	cp := *m
	cp.git = newExecGitRunner(bin)
	return &cp
}

// Create ensures lease.Holder owns exactly one worktree under repoRoot at
// the R-16.37 constants. Order (C16, intent before effect): validate,
// fence, refuse a holder the stored lease does not name, converge only on
// the holder's OWN path, write the intent row, `git worktree add`, fence
// again (compensating on failure), journal ack. Nothing is returned and
// no git command runs before the first fence and the holder check.
func (m *WorktreeManager) Create(ctx context.Context, lease ResourceLease, repoRoot string) (Worktree, error) {
	if lease.Holder == "" || repoRoot == "" {
		return Worktree{}, cascade.New(cascade.KindInvalidInput, "jobs: worktree create requires a lease holder and a repo root")
	}
	if err := m.fence(ctx, lease.RepoID, lease.ScopeGlob, lease.Epoch); err != nil {
		return Worktree{}, err
	}
	if err := requireStoredHolder(ctx, m.store, lease); err != nil {
		return Worktree{}, err
	}
	w := Worktree{
		Path: jobWorktreeDir(repoRoot, lease.Holder), LeaseRepoID: lease.RepoID, LeaseScopeGlob: lease.ScopeGlob,
		Repo: repoRoot, Branch: jobBranch(lease.Holder),
	}
	if existing, ok, err := m.convergedWorktree(ctx, w); err != nil || ok {
		return existing, err
	}
	if err := m.store.PutWorktree(ctx, w); err != nil {
		return Worktree{}, err
	}
	if err := m.appendWorktreeJournal(ctx, lease, journal.KindIntent, "create", w); err != nil {
		return Worktree{}, err
	}
	createdBranch, err := m.addWorktree(ctx, w)
	if err != nil {
		// The add failed before any effect git reports; the intent row
		// would otherwise stay pending forever.
		_ = deleteWorktreeRow(ctx, m.store, w.Path)
		return Worktree{}, err
	}
	if err := m.fence(ctx, lease.RepoID, lease.ScopeGlob, lease.Epoch); err != nil {
		return Worktree{}, m.compensateCreate(ctx, w, createdBranch, err)
	}
	if err := m.appendWorktreeJournal(ctx, lease, journal.KindAck, "created", w); err != nil {
		return Worktree{}, err
	}
	return w, nil
}

// convergedWorktree resolves the lease key's existing row against want
// (the holder's own path). ok is true only when the row IS want's path
// and that path exists. A row at want's path that is missing on disk is
// a pending intent: Create retries the add. A row at ANOTHER path belongs
// to a previous holder: a live one refuses (KindConflict, Sweep reconciles
// it), a pending one is dropped since no tree backs it.
func (m *WorktreeManager) convergedWorktree(ctx context.Context, want Worktree) (Worktree, bool, error) {
	row, ok, err := activeWorktreeRowForLease(ctx, m.store, want.LeaseRepoID, want.LeaseScopeGlob)
	if err != nil || !ok {
		return Worktree{}, false, err
	}
	_, statErr := os.Stat(row.Path)
	if row.Path != want.Path {
		if statErr == nil {
			return Worktree{}, false, cascade.Newf(cascade.KindConflict,
				"jobs: lease %s/%s still has a previous holder's worktree at %q; the sweep must reconcile it first",
				want.LeaseRepoID, want.LeaseScopeGlob, row.Path)
		}
		return Worktree{}, false, deleteWorktreeRow(ctx, m.store, row.Path)
	}
	if statErr != nil {
		return Worktree{}, false, nil // pending intent row: retry the add
	}
	return row, true, nil
}

// addWorktree runs `git worktree add` for w, creating w.Branch unless a
// pending retry finds it already present. createdBranch reports whether
// THIS call created the branch (compensation deletes only that one).
func (m *WorktreeManager) addWorktree(ctx context.Context, w Worktree) (createdBranch bool, err error) {
	refs, err := m.git.run(ctx, w.Repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+w.Branch)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(refs) != "" {
		_, err = m.runGitAdmin(ctx, w.Repo, w.Repo, "worktree", "add", w.Path, w.Branch)
		return false, err
	}
	_, err = m.runGitAdmin(ctx, w.Repo, w.Repo, "worktree", "add", w.Path, "-b", w.Branch)
	return err == nil, err
}

// compensateCreate undoes a just-added worktree whose post-add fence
// failed: `git worktree remove` (never --force), the branch only when this
// Create made it and it carries no commits of its own, then the intent
// row. It returns fenceErr unchanged on success; if a step fails the row
// stays for the sweep and the returned error still wraps fenceErr.
func (m *WorktreeManager) compensateCreate(ctx context.Context, w Worktree, createdBranch bool, fenceErr error) error {
	if _, err := m.runGitAdmin(ctx, w.Repo, w.Repo, "worktree", "remove", w.Path); err != nil {
		return cascade.Wrapf(cascade.KindConflict, fenceErr, "jobs: fenced after add; removing %q failed (%v), left for the sweep", w.Path, err)
	}
	if createdBranch {
		if _, err := m.deleteBranchIfNoOwnCommits(ctx, w.Repo, w.Branch); err != nil {
			return cascade.Wrapf(cascade.KindConflict, fenceErr, "jobs: fenced after add; deleting branch %q failed (%v)", w.Branch, err)
		}
	}
	if err := deleteWorktreeRow(ctx, m.store, w.Path); err != nil {
		return cascade.Wrapf(cascade.KindConflict, fenceErr, "jobs: fenced after add; deleting row %q failed (%v)", w.Path, err)
	}
	return fenceErr
}

// Remove removes lease's worktree: `git worktree remove <path>` plus row
// delete. It acts only on lease.Holder's OWN path (the guard Snapshot
// uses): a row at another holder's path is a no-op, so a previous
// holder's late release never touches the current holder's tree. A
// pending row (intent written, path absent) is deleted without running
// git. A dirty tree at the per-lease path refuses with a typed
// error naming the path — Remove never force-deletes; only the sweep's
// quarantine path (worktree_quarantine.go) does, and only post-move.
func (m *WorktreeManager) Remove(ctx context.Context, lease ResourceLease) error {
	row, ok, err := activeWorktreeRowForLease(ctx, m.store, lease.RepoID, lease.ScopeGlob)
	if err != nil {
		return err
	}
	if !ok || row.Path != jobWorktreeDir(row.Repo, lease.Holder) {
		return nil // nothing of this holder's to remove: converge-safe no-op
	}
	if _, statErr := os.Stat(row.Path); statErr != nil {
		return deleteWorktreeRow(ctx, m.store, row.Path)
	}
	clean, err := m.isWorktreeClean(ctx, row.Path)
	if err != nil {
		return err
	}
	if !clean {
		return cascade.Newf(cascade.KindConflict, "jobs: worktree %q has uncommitted changes; refusing to remove", row.Path)
	}
	if _, err := m.runGitAdmin(ctx, row.Repo, row.Repo, "worktree", "remove", row.Path); err != nil {
		return err
	}
	if err := deleteWorktreeRow(ctx, m.store, row.Path); err != nil {
		return err
	}
	return m.appendWorktreeJournal(ctx, lease, journal.KindAck, "removed", row)
}

// isWorktreeClean runs `git status --porcelain` inside path and reports
// whether the output is empty.
func (m *WorktreeManager) isWorktreeClean(ctx context.Context, path string) (bool, error) {
	out, err := m.git.run(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}
