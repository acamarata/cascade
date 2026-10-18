package jobs

// Purpose: PutExecutionTx, the transactional form of PutExecution, so a
//
//	caller that already holds a transaction (the fleet reservation Bind)
//	writes the execution row in that same transaction instead of on a second
//	connection.
//
// Inputs: a caller-owned *sql.Tx and one Execution.
// Outputs: the same typed errors PutExecution returns; KindInvalidInput for a
//
//	nil tx, missing ids or an unknown state, KindIntegrity for an orphan
//	execution, KindUnavailable for a storage failure.
//
// Constraints: every read and write goes through tx and never through the
//
//	Store's own *sql.DB: on a single-connection database the caller's open
//	transaction owns the only connection, so a read on the Store handle would
//	wait forever. PutExecutionTx never commits or rolls back; the caller owns
//	the transaction, and a rollback leaves no execution row.
//
// SPORT: jobs/store/ADD.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// JobDataClassTx reads a job's data class through tx, so a caller that holds
// the transaction sees the class as of that transaction and a later change
// cannot slip past a check made before it. ok is false for an unknown job.
func (*Store) JobDataClassTx(ctx context.Context, tx *sql.Tx, jobID string) (class provider.SensitivityTier, ok bool, err error) {
	if tx == nil {
		return 0, false, cascade.New(cascade.KindInvalidInput, "jobs: JobDataClassTx requires a non-nil transaction")
	}
	var name string
	err = tx.QueryRowContext(ctx, `SELECT data_class FROM `+tableJob+` WHERE id = ?`, jobID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: get job data class")
	}
	class, err = provider.ParseDataClassName(name)
	if err != nil {
		return 0, false, err
	}
	return class, true, nil
}

// PutExecutionTx inserts or replaces one execution row inside tx. It applies
// the checks PutExecution applies (ids, valid state, the job must exist) with
// the job read through tx, so a job written earlier in the same transaction
// counts. The caller commits or rolls back tx.
func (*Store) PutExecutionTx(ctx context.Context, tx *sql.Tx, e Execution) error {
	if tx == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: PutExecutionTx requires a non-nil transaction")
	}
	if e.ID == "" || e.JobID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: execution id and job_id are required")
	}
	if !e.State.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown execution state %q", string(e.State))
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+tableJob+` WHERE id = ?`, e.JobID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return cascade.Newf(cascade.KindIntegrity, "jobs: orphan execution %q: job %q does not exist", e.ID, e.JobID)
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: get job")
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO `+tableExecution+` (id, job_id, attempt, state, started_at, ended_at, pgid, heartbeat_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state, started_at=excluded.started_at,
			ended_at=excluded.ended_at, pgid=excluded.pgid, heartbeat_at=excluded.heartbeat_at`,
		e.ID, e.JobID, e.Attempt, string(e.State), e.StartedAt, e.EndedAt, e.PGID, e.HeartbeatAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put execution")
	}
	return nil
}
