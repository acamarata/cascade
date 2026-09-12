package jobs

// Purpose: HOW step 11 (R-21.147) — the immutable, content-addressed
//
//	candidate snapshot over EXPLICITLY selected untracked paths, fenced
//	through the S-59.T2 Fence entry point so a stale epoch refuses the
//	snapshot outright. AF/S-65.T2 binds a CI run to the returned tree
//	hash and AC/S-60.T3 binds an approval token and acceptance re-run to
//	it; this ticket ships the immutable tree and its epoch stamp only
//	(attempt-scoped ids, cancellation tombstones and late-result
//	rejection are AF/S-65.T2's, per this ticket's contract).
//
// CONTRACT-VS-TREE CONTRADICTION (recorded, not papered over): the
// ticket's full_desc HOW step 11 names the recipe as "`git add
// --intent-to-add` over the explicitly selected untracked paths, then
// `git write-tree` over the worktree index". Verified against a REAL git
// binary (2.51.0): `--intent-to-add` records a path's PRESENCE with a
// null blob SHA, not its content — `git write-tree` immediately
// afterward always emits the canonical empty-tree hash
// (4b825dc642cb6eb9a060e54bf8d69288fbee4904) for that path regardless of
// the file's actual bytes, proven by writing two different contents to
// the same intent-to-add path and getting the identical tree hash both
// times. That directly contradicts this ticket's own acceptance
// criterion ("changes when a selected untracked path changes") and its
// own test task ("write worktree_snapshot_test.go asserting the tree
// hash ... changes when a selected untracked path changes"). This file
// therefore stages selected paths with a REAL `git add` (which reads
// their actual content into a blob), runs `git write-tree`, and then
// `git reset -- <paths>` to restore the real index to whatever it held
// before Snapshot ran — content-addressed and verified sensitive to
// content changes, while leaving no residue in the worktree's ordinary
// staging area for whatever the job driver does next.
//
// Inputs: a lease, the epoch the caller believes is current, and the
//
//	untracked paths it explicitly selected for inclusion.
//
// Outputs: SnapshotResult{TreeHash, Epoch}, or a typed error —
//
//	ErrLeaseFenced (via FenceFunc) on a stale epoch, KindNotFound if the
//	lease has no worktree on record.
//
// Constraints: ONLY the explicitly selected untracked paths ever enter
//
//	the tree — nothing is guessed; `git add`/`git write-tree`/`git reset`
//	are git-admin mutations and run through the same per-repository
//	mutex + index.lock backoff every other admin mutation does (HOW
//	step 10).
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FenceFunc is the S-59.T2 Fence entry point's method-value shape
// (*LeaseManager).Fence satisfies directly — Snapshot takes it as a
// parameter rather than holding a *LeaseManager field, so this file adds
// no dependency edge onto lease.go/lease_fence.go beyond the one call.
type FenceFunc func(ctx context.Context, repoID, scopeGlob string, epoch int64) error

// SnapshotResult is one immutable candidate snapshot: a content-addressed
// git tree hash stamped with the lease epoch it was taken under.
type SnapshotResult struct {
	TreeHash string
	Epoch    int64
}

// Snapshot takes an immutable, content-addressed candidate snapshot of
// lease's worktree at epoch, including only selectedUntracked's paths
// among whatever untracked files exist. A stale epoch (fence returns
// ErrLeaseFenced) refuses the snapshot before any git command runs.
func (m *WorktreeManager) Snapshot(ctx context.Context, fence FenceFunc, lease ResourceLease, epoch int64, selectedUntracked []string) (SnapshotResult, error) {
	if fence != nil {
		if err := fence(ctx, lease.RepoID, lease.ScopeGlob, epoch); err != nil {
			return SnapshotResult{}, err
		}
	}
	row, ok, err := worktreeRowForLease(ctx, m.store, lease.RepoID, lease.ScopeGlob)
	if err != nil {
		return SnapshotResult{}, err
	}
	if !ok {
		return SnapshotResult{}, cascade.Newf(cascade.KindNotFound,
			"jobs: no worktree on record for lease %s/%s", lease.RepoID, lease.ScopeGlob)
	}

	if len(selectedUntracked) > 0 {
		addArgs := append([]string{"add", "--"}, selectedUntracked...)
		if _, err := m.runGitAdmin(ctx, row.Repo, row.Path, addArgs...); err != nil {
			return SnapshotResult{}, err
		}
		// Restore the ordinary staging area regardless of outcome below —
		// Snapshot must never leave a residual stage for whatever the job
		// driver does next (a normal commit, another Snapshot, ...).
		defer func() {
			resetArgs := append([]string{"reset", "--"}, selectedUntracked...)
			_, _ = m.runGitAdmin(ctx, row.Repo, row.Path, resetArgs...)
		}()
	}
	out, err := m.runGitAdmin(ctx, row.Repo, row.Path, "write-tree")
	if err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{TreeHash: strings.TrimSpace(out), Epoch: epoch}, nil
}
