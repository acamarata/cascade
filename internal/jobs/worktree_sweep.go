package jobs

// Purpose: HOW step 4 — the daemon-start orphan sweep (R-21.140/R-21.177):
//
//	reconcile every stored worktree row against its lease's state and its
//	owner execution's recorded pgid, removing ONLY a clean, dead-pgid
//	orphan (released lease, or a previous holder's tree under a lease now
//	held by another job); quarantining (worktree_quarantine.go) a dirty
//	one; deleting a pending row (intent written, tree absent) whose lease
//	is not live; and leaving every other row — a live pgid, the current
//	holder's live lease, or a lease this store no longer has a record of —
//	untouched. worktree_reconcile.go then handles job worktrees git lists
//	with no row. `git worktree prune` runs once per repo this pass
//	actually touched, clearing whatever stale admin metadata that leaves.
//
// Inputs: nothing beyond ctx — Sweep walks every row
//
//	listActiveWorktreeRows (worktree_store.go) returns.
//
// Outputs: SweepResult, the paths removed/quarantined/pruned this pass —
//
//	empty on a clean state, proving idempotency (a second Sweep over an
//	unchanged store returns a SweepResult with zero deltas by
//	construction, since a removed row's next read simply is not there).
//
// Constraints: pgid liveness gates the sweep ENTIRELY (a live pgid means
//
//	"do not touch this row", never "removed instead of quarantined") —
//	mirrors lease_fence.go's Reclaim precedent exactly: never preempt a
//	live process.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3; P1-CORE-06).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SweepResult reports one Sweep pass's outcome.
type SweepResult struct {
	Removed     []string // worktree paths removed (released, clean, dead pgid)
	Quarantined []string // worktree paths moved to quarantine (released, dirty, dead pgid)
	Pruned      []string // repo roots `git worktree prune` ran against
}

// Sweep runs one daemon-start reconciliation pass over every stored
// worktree row, then reconciles `git worktree list` against the rows of
// every repository the store knows (worktree_reconcile.go). See this
// file's package doc for the exact eligibility rule.
func (m *WorktreeManager) Sweep(ctx context.Context) (SweepResult, error) {
	repos, err := listWorktreeRepos(ctx, m.store)
	if err != nil {
		return SweepResult{}, err
	}
	rows, err := listActiveWorktreeRows(ctx, m.store)
	if err != nil {
		return SweepResult{}, err
	}

	var result SweepResult
	touched := map[string]bool{}
	for _, row := range rows {
		if err := m.sweepRow(ctx, row, &result, touched); err != nil {
			return result, err
		}
	}
	for _, repoRoot := range repos {
		if err := m.reconcileRepo(ctx, repoRoot, &result, touched); err != nil {
			return result, err
		}
	}
	for repoRoot := range touched {
		if _, err := m.runGitAdmin(ctx, repoRoot, repoRoot, "worktree", "prune"); err != nil {
			return result, err
		}
		result.Pruned = append(result.Pruned, repoRoot)
	}
	return result, nil
}

// leaseIsLive reports whether a lease may still have a holder acting under
// it: held, renewing, or expired_unconfirmed (termination not confirmed).
func leaseIsLive(l ResourceLease) bool {
	return l.State == LeaseHeld || l.State == LeaseRenewing || l.State == LeaseExpiredUnconfirmed
}

// sweepRow applies the eligibility rule to one row, mutating result and
// touched on a removal or quarantine. The row's owner is the job id its
// path names; when that differs from the lease's current holder the row
// is a previous holder's orphan, even though the lease itself is live.
func (m *WorktreeManager) sweepRow(ctx context.Context, row Worktree, result *SweepResult, touched map[string]bool) error {
	lease, found, err := m.store.GetLease(ctx, row.LeaseRepoID, row.LeaseScopeGlob)
	if err != nil {
		return err
	}
	owner := worktreeOwner(row.Path)
	previousHolder := found && owner != lease.Holder
	if _, statErr := os.Stat(row.Path); statErr != nil {
		if found && leaseIsLive(lease) && !previousHolder {
			return nil // the live holder's pending intent row: Create retries its add
		}
		// Pending under a dead lease, or the tree was removed outside this
		// manager: only the row is stale (plus git's admin entry, so this
		// repo still needs a prune pass).
		touched[row.Repo] = true
		result.Removed = append(result.Removed, row.Path)
		return deleteWorktreeRow(ctx, m.store, row.Path)
	}
	if !found || (lease.State != LeaseReleased && !previousHolder) {
		return nil // missing lease or the current holder's non-released lease: never touched (fail-closed)
	}
	orphan := lease
	orphan.Holder = owner
	return m.sweepOrphan(ctx, orphan, row, result, touched)
}

// sweepOrphan removes (clean) or quarantines (dirty) one orphaned tree
// whose owner's recorded pgid is not alive; a live pgid blocks it, and so
// does a HEAD no ref contains (worktree_reconcile.go headReachableFromRefs).
func (m *WorktreeManager) sweepOrphan(ctx context.Context, owner ResourceLease, row Worktree, result *SweepResult, touched map[string]bool) error {
	pgid, havePGID, err := latestExecutionPGID(ctx, m.store, owner.Holder)
	if err != nil {
		return err
	}
	if havePGID && m.probe.IsAlive(pgid) {
		return nil // a live pgid blocks the sweep entirely
	}
	if safe, err := m.headReachableFromRefs(ctx, row.Path); err != nil || !safe {
		return err // HEAD's commit is on no ref: removal would orphan it, so the row stays
	}
	porcelain, err := m.git.run(ctx, row.Path, "status", "--porcelain")
	if err != nil {
		return err
	}
	touched[row.Repo] = true
	if strings.TrimSpace(porcelain) == "" {
		return m.removeSweptRow(ctx, owner, row, result)
	}
	if err := m.quarantine(ctx, owner, row, pgidProbeDescription(havePGID, pgid), dirtyFileCount(porcelain)); err != nil {
		return err
	}
	result.Quarantined = append(result.Quarantined, row.Path)
	return nil
}

// removeSweptRow removes a verified-clean, released, dead-pgid orphan.
func (m *WorktreeManager) removeSweptRow(ctx context.Context, lease ResourceLease, row Worktree, result *SweepResult) error {
	if _, err := m.runGitAdmin(ctx, row.Repo, row.Repo, "worktree", "remove", row.Path); err != nil {
		return err
	}
	if err := deleteWorktreeRow(ctx, m.store, row.Path); err != nil {
		return err
	}
	if err := m.appendWorktreeJournal(ctx, lease, journal.KindAck, "swept", row); err != nil {
		return err
	}
	result.Removed = append(result.Removed, row.Path)
	return nil
}

// pgidProbeDescription renders the probe outcome for the quarantine
// journal's "pgid probe result" field.
func pgidProbeDescription(havePGID bool, pgid int64) string {
	if !havePGID {
		return "no-pgid-recorded"
	}
	return fmt.Sprintf("dead:%d", pgid)
}

// latestExecutionPGID returns jobID's most recently started execution's
// pgid (0/false if none, or none recorded) — the same read
// lease_fence.go's latestExecutionPGIDTx performs, without a transaction
// (Sweep has no multi-statement atomicity requirement over this read).
func latestExecutionPGID(ctx context.Context, s *Store, jobID string) (int64, bool, error) {
	var pgid sql.NullInt64
	row := s.db.QueryRowContext(ctx,
		`SELECT pgid FROM `+tableExecution+` WHERE job_id = ? ORDER BY attempt DESC LIMIT 1`, jobID)
	err := row.Scan(&pgid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read holder execution pgid")
	}
	if !pgid.Valid || pgid.Int64 == 0 {
		return 0, false, nil
	}
	return pgid.Int64, true, nil
}
