package jobs

import (
	"context"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReviewerTreeForJob returns the clean, fenced snapshot tree for jobID's
// single held or renewing lease. Missing leases or worktrees are not found;
// ambiguous leases and dirty worktrees are conflicts. Ignored files are allowed.
// Dirty worktrees wrap ErrReviewerMutatedWorktree from reviewer_policy.go.
// It never stages, resets, or commits reviewer changes.
func (m *WorktreeManager) ReviewerTreeForJob(ctx context.Context, jobID string) (string, error) {
	lease, err := m.reviewerLeaseForJob(ctx, jobID)
	if err != nil {
		return "", err
	}
	if err := m.fence(ctx, lease.RepoID, lease.ScopeGlob, lease.Epoch); err != nil {
		return "", err
	}
	row, ok, err := activeWorktreeRowForLease(ctx, m.store, lease.RepoID, lease.ScopeGlob)
	if err != nil {
		return "", err
	}
	if !ok || row.Path != jobWorktreeDir(row.Repo, jobID) {
		return "", cascade.Newf(cascade.KindNotFound, "jobs: no reviewer worktree for job %q", jobID)
	}
	if _, err := os.Stat(row.Path); err != nil {
		return "", cascade.Wrapf(cascade.KindNotFound, err, "jobs: reviewer worktree %q is absent", row.Path)
	}
	status, err := m.git.run(ctx, row.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", cascade.Wrapf(cascade.KindConflict, ErrReviewerMutatedWorktree,
			"jobs: reviewer worktree %q has uncommitted changes", row.Path)
	}
	snapshot, err := m.Snapshot(ctx, nil, lease, lease.Epoch, nil)
	if err != nil {
		return "", err
	}
	return snapshot.TreeHash, nil
}

// reviewerLeaseForJob resolves exactly one active lease without acquiring or
// changing any lease. Released and expired leases cannot authorize a reviewer.
func (m *WorktreeManager) reviewerLeaseForJob(ctx context.Context, jobID string) (ResourceLease, error) {
	leases, err := m.store.allLeasesForHolder(ctx, jobID)
	if err != nil {
		return ResourceLease{}, err
	}
	var selected ResourceLease
	count := 0
	for _, lease := range leases {
		if lease.State != LeaseHeld && lease.State != LeaseRenewing {
			continue
		}
		selected = lease
		count++
	}
	if count == 0 {
		return ResourceLease{}, cascade.Newf(cascade.KindNotFound, "jobs: no active reviewer lease for job %q", jobID)
	}
	if count != 1 {
		return ResourceLease{}, cascade.Newf(cascade.KindConflict, "jobs: multiple active reviewer leases for job %q", jobID)
	}
	return selected, nil
}
