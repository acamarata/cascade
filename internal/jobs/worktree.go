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
}

// NewWorktreeManager constructs a WorktreeManager over store, shelling out
// to the git binary found on PATH. j and attn may be nil to disable their
// respective integrations (unit tests). A nil probe defaults to
// NewProcessLivenessProbe() (lease_fence_unix.go/lease_fence_windows.go's
// real per-platform signal/handle check) — pass a fake probe in sweep
// tests for deterministic pgid-liveness outcomes.
func NewWorktreeManager(store *Store, j journal.Store, attn *supervision.Store, probe ProcessLivenessProbe) *WorktreeManager {
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
	}
}

// withGitBinary returns a copy of m shelling out to bin instead of the
// PATH-resolved git — the injected-binary-path seam error-path tests use.
func (m *WorktreeManager) withGitBinary(bin string) *WorktreeManager {
	cp := *m
	cp.git = newExecGitRunner(bin)
	return &cp
}

// Create ensures lease.Holder owns exactly one worktree under repoRoot at
// the R-16.37 constants, converging on an existing live worktree for the
// same lease (no second `git worktree add`, no error) rather than
// re-adding it.
func (m *WorktreeManager) Create(ctx context.Context, lease ResourceLease, repoRoot string) (Worktree, error) {
	if lease.Holder == "" || repoRoot == "" {
		return Worktree{}, cascade.New(cascade.KindInvalidInput, "jobs: worktree create requires a lease holder and a repo root")
	}
	if existing, ok, err := m.convergedWorktree(ctx, lease); err != nil {
		return Worktree{}, err
	} else if ok {
		return existing, nil
	}

	path := jobWorktreeDir(repoRoot, lease.Holder)
	branch := jobBranch(lease.Holder)
	if _, err := m.runGitAdmin(ctx, repoRoot, repoRoot, "worktree", "add", path, "-b", branch); err != nil {
		return Worktree{}, err
	}

	w := Worktree{Path: path, LeaseRepoID: lease.RepoID, LeaseScopeGlob: lease.ScopeGlob, Repo: repoRoot, Branch: branch}
	if err := m.store.PutWorktree(ctx, w); err != nil {
		return Worktree{}, err
	}
	if err := m.appendWorktreeJournal(ctx, lease, journal.KindIntent, "created", w); err != nil {
		return Worktree{}, err
	}
	return w, nil
}

// convergedWorktree reports the existing worktree row for lease, IFF one
// is on record AND its path still exists on disk.
func (m *WorktreeManager) convergedWorktree(ctx context.Context, lease ResourceLease) (Worktree, bool, error) {
	row, ok, err := worktreeRowForLease(ctx, m.store, lease.RepoID, lease.ScopeGlob)
	if err != nil || !ok {
		return Worktree{}, false, err
	}
	if _, statErr := os.Stat(row.Path); statErr != nil {
		return Worktree{}, false, nil
	}
	return row, true, nil
}

// Remove removes lease's worktree: `git worktree remove <path>` plus row
// delete. A dirty tree at the per-lease path refuses with a typed error
// naming the path — Remove never force-deletes; only the sweep's
// quarantine path (worktree_quarantine.go) does, and only post-move.
func (m *WorktreeManager) Remove(ctx context.Context, lease ResourceLease) error {
	row, ok, err := worktreeRowForLease(ctx, m.store, lease.RepoID, lease.ScopeGlob)
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing to remove: converge-safe no-op
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
