package jobs

// Purpose: AUD-004's reconciliation half of the sweep. Rows are written
//
//	before `git worktree add` (worktree.go's intent row), so any job
//	worktree git reports under <repo>/.cascade/worktrees that has NO row
//	is an unrecorded tree: an interrupted compensation, a crash in a
//	pre-intent-row build, or an operator's hand-made tree. reconcileRepo
//	removes a clean one (deleting its job branch only when the branch
//	carries no commits of its own) and quarantines a dirty one through
//	worktree_quarantine.go, never a delete.
//
// Inputs: the repository roots the store knows (listWorktreeRepos, read
//
//	before the row pass deletes anything) and `git worktree list
//	--porcelain` per root (worktree_list.go).
//
// Outputs: SweepResult deltas and the touched-repo set Sweep prunes.
//
// Constraints: a locked entry, a non-job path, or a job whose recorded
//
//	pgid is alive is never touched. Branch deletion holds the repo's
//	admin mutex (worktree_mutex.go) across the commit check and the
//	delete. A repository reachable only through a lease, with no row at
//	all, is outside this pass: no row names its filesystem root.
//
// SPORT: jobs/worktree-manager (P1-CORE-06).

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// worktreeOwner returns the job id a worktree path names (job-<id>), or ""
// for a path this manager did not name.
func worktreeOwner(path string) string {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "job-") {
		return ""
	}
	return strings.TrimPrefix(base, "job-")
}

// canonicalPath resolves symlinks in path's parent directory (the leaf may
// already be gone) so a stored row path and git's realpath output compare
// equal (macOS /var -> /private/var).
func canonicalPath(path string) string {
	clean := filepath.Clean(path)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil {
		return filepath.Join(dir, filepath.Base(clean))
	}
	return clean
}

// listWorktreeRepos returns every distinct repo root on any worktree row,
// quarantined rows included.
func listWorktreeRepos(ctx context.Context, s *Store) ([]string, error) {
	return queryStrings(ctx, s, `SELECT DISTINCT repo FROM `+tableWorktree+` ORDER BY repo`)
}

// recordedWorktreePaths returns the canonical path of every worktree row.
func recordedWorktreePaths(ctx context.Context, s *Store) (map[string]bool, error) {
	paths, err := queryStrings(ctx, s, `SELECT path FROM `+tableWorktree)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[canonicalPath(p)] = true
	}
	return out, nil
}

func queryStrings(ctx context.Context, s *Store, query string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: query worktree rows")
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan worktree row")
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate worktree rows")
	}
	return out, nil
}

// activeWorktreeRowForLease returns the lease key's row that is NOT parked
// under quarantine. A quarantined row keeps its lease key (the quarantine
// move re-keys only the path), so worktreeRowForLease alone could hand a
// later holder of the same lease the quarantined tree.
func activeWorktreeRowForLease(ctx context.Context, s *Store, repoID, scopeGlob string) (Worktree, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, lease_repo_id, lease_scope_glob, repo, branch FROM `+tableWorktree+`
		 WHERE lease_repo_id = ? AND lease_scope_glob = ? ORDER BY path`, repoID, scopeGlob)
	if err != nil {
		return Worktree{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: query worktree for lease")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var w Worktree
		if err := rows.Scan(&w.Path, &w.LeaseRepoID, &w.LeaseScopeGlob, &w.Repo, &w.Branch); err != nil {
			return Worktree{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan worktree row")
		}
		if !isQuarantinedPath(w.Path) {
			return w, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Worktree{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate worktree rows")
	}
	return Worktree{}, false, nil
}

// reconcileRepo compares git's worktree list for repoRoot against the
// stored rows and handles every unrecorded job worktree. A repo root that
// no longer exists on disk has nothing left to reconcile.
func (m *WorktreeManager) reconcileRepo(ctx context.Context, repoRoot string, result *SweepResult, touched map[string]bool) error {
	if _, err := os.Stat(repoRoot); err != nil {
		return nil
	}
	entries, err := m.listGitWorktrees(ctx, repoRoot)
	if err != nil {
		return err
	}
	recorded, err := recordedWorktreePaths(ctx, m.store)
	if err != nil {
		return err
	}
	jobsDir := canonicalPath(filepath.Join(repoRoot, filepath.FromSlash(worktreesDirName)))
	for _, e := range entries {
		path := canonicalPath(e.Path)
		if e.Locked || filepath.Dir(path) != jobsDir || worktreeOwner(path) == "" || recorded[path] {
			continue
		}
		if err := m.reconcileEntry(ctx, repoRoot, path, e.Branch, result, touched); err != nil {
			return err
		}
	}
	return nil
}

// reconcileEntry removes (clean) or quarantines (dirty) one unrecorded job
// worktree at path. A path already gone only needs the prune pass. A tree
// whose HEAD no ref contains (a detached commit) is left in place: both
// the removal and quarantine's admin detach would drop the only pointer
// to that commit.
func (m *WorktreeManager) reconcileEntry(ctx context.Context, repoRoot, path, branch string, result *SweepResult, touched map[string]bool) error {
	owner := worktreeOwner(path)
	if pgid, ok, err := latestExecutionPGID(ctx, m.store, owner); err != nil || (ok && m.probe.IsAlive(pgid)) {
		return err // a live pgid blocks the sweep entirely
	}
	touched[repoRoot] = true
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	if safe, err := m.headReachableFromRefs(ctx, path); err != nil || !safe {
		return err // removing or quarantining would orphan HEAD's commit: left in place
	}
	porcelain, err := m.git.run(ctx, path, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(porcelain) != "" {
		unrecorded := ResourceLease{RepoID: repoRoot, Holder: owner, State: LeaseReleased}
		row := Worktree{Path: path, LeaseRepoID: repoRoot, Repo: repoRoot, Branch: branch}
		if err := m.quarantine(ctx, unrecorded, row, "unrecorded", dirtyFileCount(porcelain)); err != nil {
			return err
		}
		result.Quarantined = append(result.Quarantined, path)
		return nil
	}
	if _, err := m.runGitAdmin(ctx, repoRoot, repoRoot, "worktree", "remove", path); err != nil {
		return err
	}
	if branch == jobBranch(owner) {
		if _, err := m.deleteBranchIfNoOwnCommits(ctx, repoRoot, branch); err != nil {
			return err
		}
	}
	result.Removed = append(result.Removed, path)
	return nil
}

// headReachableFromRefs reports whether the commit checked out at path is
// contained in some branch, tag or remote ref, so dropping the tree's own
// HEAD loses no commit. A tree on a branch passes through that branch,
// which removal keeps (only deleteBranchIfNoOwnCommits deletes one, under
// its own check); a detached HEAD passes only when a ref contains it.
func (m *WorktreeManager) headReachableFromRefs(ctx context.Context, path string) (bool, error) {
	out, err := m.git.run(ctx, path, "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "0", nil
}

// deleteBranchIfNoOwnCommits deletes refs/heads/<branch> only when every
// commit on it is reachable from another branch, tag or remote ref, so the
// delete can lose no work. The check and the delete run under repoRoot's
// admin mutex so no other admin mutation interleaves.
func (m *WorktreeManager) deleteBranchIfNoOwnCommits(ctx context.Context, repoRoot, branch string) (bool, error) {
	mu := m.mutexes.forRepo(repoRoot)
	mu.Lock()
	defer mu.Unlock()
	ref := "refs/heads/" + branch
	// --exclude before --branches takes the short name (git-rev-list(1)).
	out, err := m.git.run(ctx, repoRoot, "rev-list", "--count", ref, "--not", "--exclude="+branch, "--branches", "--tags", "--remotes")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) != "0" {
		return false, nil // the branch carries commits of its own: it survives
	}
	if _, err := m.git.run(ctx, repoRoot, "branch", "-D", branch); err != nil {
		return false, err
	}
	return true, nil
}
