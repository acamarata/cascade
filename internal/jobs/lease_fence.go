package jobs

// Purpose: R-21.139/R-21.177's fence and deterministic reclaim: Fence,
//
//	the SINGLE validation entry point every lease-scoped mutation (T3
//	worktree add/remove, AF/S-65.T2 job-branch commit/CI dispatch,
//	AH/S-70.T2 integration/release, S-60.T3 evidence write) presents its
//	believed epoch to; and Reclaim, the pgid-liveness-probe path that
//	moves a lease stuck in expired_unconfirmed to expired_orphaned once
//	termination is CONFIRMED, never before.
//
// Inputs: a (repoID, scopeGlob, presentedEpoch) triple (Fence); a
//
//	ProcessLivenessProbe (Reclaim) -- the injected liveness seam this
//	file's platform-specific companions (lease_fence_unix.go,
//	lease_fence_windows.go) implement, so tests drive Reclaim
//	deterministically against a fake probe rather than a real process.
//
// Outputs: nil on a valid fence; typed ErrLeaseFenced (plus exactly one
//
//	attention item via the sink) on a mismatch. Reclaim returns the
//	lease's row after whichever transition (or non-transition) applied.
//
// Constraints: a LIVE pgid is never preempted -- Reclaim leaves the row
//
//	in expired_unconfirmed and the contender stays queued. The epoch
//	advances ONLY inside a confirmed-dead Reclaim or a fresh Acquire
//	grant, never inside Fence itself (a fence never advances the epoch
//	it is validating against).
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrLeaseFenced is returned by Fence (and by Release, per R-21.193's
// stall-release co-ownership) when the caller's presented epoch does not
// match the lease's current stored epoch.
var ErrLeaseFenced = cascade.New(cascade.KindConflict, "jobs: lease epoch fence mismatch")

// ProcessLivenessProbe abstracts "is this recorded pgid still a live
// process" so Reclaim's tests are deterministic (a fake probe) while
// production wires the real signal-0/handle check. 0 or a negative pgid
// is never alive by construction -- a probe implementation need not
// special-case it, but Reclaim checks it first regardless so a caller
// that supplies a nil probe for a job with no recorded pgid still gets a
// deterministic "not alive" rather than a nil-pointer panic.
type ProcessLivenessProbe interface {
	IsAlive(pgid int64) bool
}

// Fence is the single validation entry point every lease-scoped mutation
// presents its believed epoch to. A mismatch (including "no such lease")
// refuses with ErrLeaseFenced and raises exactly one attention item
// (idempotent on (kind, source_ref) = (stall, this lease's entity id),
// the same rule step 6's expiry attention uses) through m's sink, if any.
func (m *LeaseManager) Fence(ctx context.Context, repoID, scopeGlob string, epoch int64) error {
	lease, ok, err := m.store.GetLease(ctx, repoID, scopeGlob)
	if err != nil {
		return err
	}
	if !ok || lease.Epoch != epoch {
		if m.sink != nil {
			if sinkErr := m.sink.fenced(ctx, repoID, scopeGlob, epoch); sinkErr != nil {
				return sinkErr
			}
		}
		return cascade.Wrapf(cascade.KindConflict, ErrLeaseFenced,
			"jobs: fence check on %s/%s presented epoch %d", repoID, scopeGlob, epoch)
	}
	return nil
}

// Reclaim runs the R-21.177 deterministic reclaim for one lease: a
// no-op unless the lease is currently expired_unconfirmed; otherwise it
// probes the holder's most recent recorded execution pgid through
// probe. A dead pgid moves the row to expired_orphaned, journals the
// probe result, and advances the epoch (the row is now non-contending --
// Contending() excludes expired_orphaned -- so a queued caller's next
// Acquire succeeds). A LIVE pgid changes nothing: the row stays
// expired_unconfirmed and the contender stays queued.
func (m *LeaseManager) Reclaim(ctx context.Context, repoID, scopeGlob string, probe ProcessLivenessProbe) (ResourceLease, error) {
	var result ResourceLease
	txErr := m.store.withTx(ctx, func(tx *sql.Tx) error {
		lease, ok, err := getLeaseTx(ctx, tx, repoID, scopeGlob)
		if err != nil {
			return err
		}
		if !ok || lease.State != LeaseExpiredUnconfirmed {
			result = lease
			return nil
		}
		pgid, havePGID, err := latestExecutionPGIDTx(ctx, tx, lease.Holder)
		if err != nil {
			return err
		}
		alive := havePGID && probe != nil && probe.IsAlive(pgid)
		if alive {
			result = lease
			return nil
		}
		nextEpoch, err := nextEpochTx(ctx, tx, repoID, scopeGlob)
		if err != nil {
			return err
		}
		lease.State = LeaseExpiredOrphaned
		lease.Epoch = nextEpoch
		if err := putLeaseTx(ctx, tx, lease); err != nil {
			return err
		}
		result = lease
		return nil
	})
	if txErr != nil {
		return ResourceLease{}, txErr
	}
	if result.State == LeaseExpiredOrphaned && m.sink != nil {
		if err := m.sink.reclaimed(ctx, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ReclaimAll runs Reclaim over every currently expired_unconfirmed lease
// across every repo -- the daemon-start sweep R-21.177 describes.
func (m *LeaseManager) ReclaimAll(ctx context.Context, probe ProcessLivenessProbe) ([]ResourceLease, error) {
	rows, err := m.store.db.QueryContext(ctx,
		`SELECT repo_id, scope_glob FROM `+tableResourceLease+` WHERE state = ?`, string(LeaseExpiredUnconfirmed))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list expired_unconfirmed leases")
	}
	type key struct{ repoID, scope string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.repoID, &k.scope); err != nil {
			_ = rows.Close()
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan expired_unconfirmed lease")
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate expired_unconfirmed leases")
	}
	_ = rows.Close()

	var out []ResourceLease
	for _, k := range keys {
		lease, err := m.Reclaim(ctx, k.repoID, k.scope, probe)
		if err != nil {
			return out, err
		}
		out = append(out, lease)
	}
	return out, nil
}

// latestExecutionPGIDTx returns the most recently started execution's
// pgid for jobID, inside tx. ok is false when jobID has no execution row
// or its pgid was never recorded (0, the "not recorded" sentinel
// model.go's Execution.PGID doc comment names).
func latestExecutionPGIDTx(ctx context.Context, tx *sql.Tx, jobID string) (int64, bool, error) {
	var pgid sql.NullInt64
	row := tx.QueryRowContext(ctx,
		`SELECT pgid FROM `+tableExecution+` WHERE job_id = ? ORDER BY attempt DESC LIMIT 1`, jobID)
	err := row.Scan(&pgid)
	if err == sql.ErrNoRows {
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
