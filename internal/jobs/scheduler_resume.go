package jobs

// Purpose: HOW 3's Resume -- called once at daemon start (after the
//
//	C/S-05.T3 crash-safety scan): reconciles the outbox by idempotency
//	key, applies the HOW 7 heartbeat reaper, then scans `running` jobs
//	whose lease has gone stale (ttl+expiry_grace elapsed) and re-enters
//	them at `leased`.
//
// Inputs: an open, migrated *Store, a Reader wired to the real M/S-27.T1
//
//	journal, a heartbeatInterval (AF/S-65.T2 owns the writer; this
//	ticket only owns the reap threshold math), and a SiteProbe per
//	OutboxSite so Reconcile can ask "does the effect already exist"
//	without importing any site's own package.
//
// Outputs: the re-entry Events for the coordinator to feed back into
//
//	Advance, or a typed error.
//
// Constraints: Resume runs only on the controller (Guard, R-21.169); a
//
//	job whose lease is expired_unconfirmed is left alone (R-21.139/
//	R-21.177 -- only the S-59.T2 reclaim path may move it).
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5).

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SiteProbe answers, for one unconfirmed outbox row, whether the effect
// it describes already happened out in the world (a live process for
// the recorded pgid, an existing lease row at the recorded epoch, a
// journaled inbox item id, a CI run id, or an integration branch
// commit -- HOW 5's five site-specific checks). true means "confirm
// without re-performing"; false means "re-perform under the same key".
type SiteProbe func(ctx context.Context, row OutboxRow) (exists bool, err error)

// CompensateFn undoes a decided-but-unconfirmed effect for a job that
// reached a terminal state before the row was confirmed (HOW 5's
// TERMINAL-STATE PRECEDENCE: release the lease, cancel the CI run --
// whatever site.CompensateFn is wired to). Never called for a
// non-terminal job.
type CompensateFn func(ctx context.Context, row OutboxRow) error

// ReconcileResult reports what Reconcile decided for each unconfirmed
// row.
type ReconcileResult struct {
	Confirmed   []string // row IDs whose effect already existed
	ToReperform []string // row IDs whose effect must be re-performed under the same key
	Compensated []string // row IDs whose owning job was terminal: compensated, never advanced
}

// Resume implements HOW 3: reconcile the outbox, reap stale heartbeats,
// then re-enter stale `running` jobs at `leased`.
func (s *Scheduler) Resume(ctx context.Context, store *Store, journal Reader, heartbeatInterval time.Duration, probes map[OutboxSite]SiteProbe, compensate map[OutboxSite]CompensateFn) ([]Event, error) {
	if err := requireControllerCtx(ctx, "job.resume"); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: scheduler: Resume requires a non-nil Store")
	}

	now := s.now()
	if _, err := s.reconcileOutbox(ctx, store, probes, compensate, now); err != nil {
		return nil, err
	}
	if err := s.reapHeartbeats(ctx, store, heartbeatInterval, now); err != nil {
		return nil, err
	}
	return s.reenterStaleJobs(ctx, store, journal, now)
}

// now reads s.clock, defaulting to the zero instant only if unset (a nil
// clock is a caller bug this returns a deterministic, not-yet-observed
// value for, rather than panicking mid-resume).
func (s *Scheduler) now() int64 {
	if s.clock == nil {
		return 0
	}
	return s.clock.Now().Unix()
}

// reconcileOutbox implements HOW 3(a) and HOW 5's TERMINAL-STATE
// PRECEDENCE: every intent/effect row is resolved by key before any
// re-entry event is emitted. A row whose owning job already reached a
// terminal state is compensated and never advanced, even if the probe
// would otherwise say the effect exists.
func (s *Scheduler) reconcileOutbox(ctx context.Context, store *Store, probes map[OutboxSite]SiteProbe, compensate map[OutboxSite]CompensateFn, now int64) (ReconcileResult, error) {
	var result ReconcileResult
	err := store.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := unconfirmedOutboxRowsTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := reconcileOneRow(ctx, tx, row, probes, compensate, now, &result); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// reconcileOneRow is reconcileOutbox's per-row body (funlen-cap split).
func reconcileOneRow(ctx context.Context, tx *sql.Tx, row OutboxRow, probes map[OutboxSite]SiteProbe, compensate map[OutboxSite]CompensateFn, now int64, result *ReconcileResult) error {
	job, ok, err := getJobTx(ctx, tx, row.JobID)
	if err == nil && ok && job.State.Terminal() {
		if fn := compensate[row.Site]; fn != nil {
			if err := fn(ctx, row); err != nil {
				return err
			}
		}
		if err := transitionOutboxTx(ctx, tx, now, row.IdempotencyKey, OutboxReconciled); err != nil {
			return err
		}
		result.Compensated = append(result.Compensated, row.ID)
		return nil
	}
	probe := probes[row.Site]
	if probe == nil {
		return nil // no probe wired for this site yet: leave the row for a later Resume
	}
	exists, err := probe(ctx, row)
	if err != nil {
		return err
	}
	if exists {
		if err := ConfirmEffect(ctx, tx, now, row.IdempotencyKey); err != nil {
			return err
		}
		result.Confirmed = append(result.Confirmed, row.ID)
		return nil
	}
	result.ToReperform = append(result.ToReperform, row.ID)
	return nil
}

// getJobTx reads one job row inside tx, matching store_job.go's GetJob
// shape but transaction-scoped (store_job.go's own GetJob runs directly
// against s.db, not a caller-supplied tx).
func getJobTx(ctx context.Context, tx *sql.Tx, id string) (Job, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, state FROM `+tableJob+` WHERE id = ?`, id)
	var j Job
	var state string
	if err := row.Scan(&j.ID, &state); err != nil {
		if err == sql.ErrNoRows {
			return Job{}, false, nil
		}
		return Job{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: read job for reconciliation")
	}
	j.State = JobState(state)
	return j, true, nil
}

// reapHeartbeats implements HOW 7: an execution whose heartbeat_at is
// older than three heartbeatInterval periods moves to abandoned, its
// result is rejected, and its job's lease enters the reclaim path
// (marked expired_unconfirmed here; the S-59.T2 pgid-probe sweep is what
// later confirms holder death and advances the epoch).
func (s *Scheduler) reapHeartbeats(ctx context.Context, store *Store, heartbeatInterval time.Duration, now int64) error {
	if heartbeatInterval <= 0 {
		return nil // no interval configured: nothing to reap against
	}
	staleBefore := now - int64(3*heartbeatInterval/time.Second)
	return store.withTx(ctx, func(tx *sql.Tx) error {
		stale, err := staleRunningExecutionsTx(ctx, tx, staleBefore)
		if err != nil {
			return err
		}
		for _, execID := range stale {
			if err := abandonExecutionTx(ctx, tx, execID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func staleRunningExecutionsTx(ctx context.Context, tx *sql.Tx, staleBefore int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM `+tableExecution+` WHERE state = ? AND heartbeat_at > 0 AND heartbeat_at < ?`,
		string(ExecutionRunning), staleBefore)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: list stale executions")
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: scan stale execution id")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: iterate stale executions")
	}
	return ids, nil
}

func abandonExecutionTx(ctx context.Context, tx *sql.Tx, execID string, now int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE `+tableExecution+` SET state = ?, ended_at = ? WHERE id = ?`,
		string(ExecutionAbandoned), now, execID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: abandon execution")
	}
	// Reject the result row, if one exists yet (an abandoned execution may
	// not have produced a result row at all).
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableExecutionResult+` SET error_kind = ?, error_message = ? WHERE execution_id = ?`,
		string(cascade.KindTimeout), "execution abandoned: heartbeat exceeded reap threshold", execID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: reject abandoned execution result")
	}
	return nil
}
