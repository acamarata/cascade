package jobs

// Purpose: HOW step 4 — the daemon-start orphan sweep (R-21.140/R-21.177):
//
//	reconcile every stored worktree row against its lease's state and its
//	holder execution's recorded pgid, removing ONLY a released, clean,
//	dead-pgid orphan; quarantining (worktree_quarantine.go) a released,
//	dead-pgid, DIRTY orphan; and leaving every other row — a live pgid, an
//	expired_unconfirmed lease, or a lease this store no longer has a
//	record of — untouched. `git worktree prune` runs once per repo this
//	pass actually removed or quarantined something in, clearing whatever
//	stale admin metadata that leaves behind.
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
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

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
// worktree row. See this file's package doc for the exact eligibility
// rule.
func (m *WorktreeManager) Sweep(ctx context.Context) (SweepResult, error) {
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
	for repoRoot := range touched {
		if _, err := m.runGitAdmin(ctx, repoRoot, repoRoot, "worktree", "prune"); err != nil {
			return result, err
		}
		result.Pruned = append(result.Pruned, repoRoot)
	}
	return result, nil
}

// sweepRow applies the eligibility rule to one row, mutating result and
// touched on a removal or quarantine.
func (m *WorktreeManager) sweepRow(ctx context.Context, row Worktree, result *SweepResult, touched map[string]bool) error {
	lease, found, err := m.store.GetLease(ctx, row.LeaseRepoID, row.LeaseScopeGlob)
	if err != nil {
		return err
	}
	if !found || lease.State != LeaseReleased {
		return nil // missing lease or any non-released state: never touched (fail-closed)
	}

	pgid, havePGID, err := latestExecutionPGID(ctx, m.store, lease.Holder)
	if err != nil {
		return err
	}
	alive := havePGID && m.probe.IsAlive(pgid)
	if alive {
		return nil // a live pgid blocks the sweep entirely
	}
	probeResult := pgidProbeDescription(havePGID, pgid)

	if _, statErr := os.Stat(row.Path); statErr != nil {
		// The tree is already gone from disk (e.g. removed outside this
		// manager) -- only the row is stale, but git's own admin metadata
		// for it is stale too, so this repo still needs a prune pass.
		touched[row.Repo] = true
		result.Removed = append(result.Removed, row.Path)
		return deleteWorktreeRow(ctx, m.store, row.Path)
	}

	porcelain, err := m.git.run(ctx, row.Path, "status", "--porcelain")
	if err != nil {
		return err
	}
	touched[row.Repo] = true
	if strings.TrimSpace(porcelain) == "" {
		return m.removeSweptRow(ctx, lease, row, result)
	}
	if err := m.quarantine(ctx, lease, row, probeResult, dirtyFileCount(porcelain)); err != nil {
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
