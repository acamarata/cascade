package jobs

// Purpose: R-16.82's restricted apply path for Resume's running->leased
//
//	re-entry. scheduler_resume.go's reenterStaleJobs EMITS a
//	JobTransitioned{running, leased} event; nothing applied it
//	(DEFECT-resume-transition-not-applied). PutResumeTransition is the
//	apply step: it writes the job's stored state, gated by the same
//	R-21.169 controller guard Resume itself already checks, and it
//	re-checks the expired_unconfirmed precondition AT the transition
//	rather than trusting the caller's earlier (upstream) check of the
//	same condition.
//
// Inputs: an open, migrated *Store, a controller-role ctx, and jobID.
// Outputs: the job's stored state becomes `leased`, or a typed error;
//
//	never a silent no-op.
//
// Constraints: never reachable from the public path -- PutTransition
//
//	(store_job.go) stays the only public mutator of Job.State, and
//	ResumeTransitionAllowed only ever permits running->leased. Refuses
//	when the job's holder has an expired_unconfirmed lease: R-21.139/
//	R-21.177 reserve advancing that lease's epoch to the S-59.T2 reclaim
//	path alone.
//
// SPORT: jobs/scheduler/FIX (R-16.82, DEFECT-resume-transition-not-applied).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PutResumeTransition applies the one resume-only transition (running ->
// leased) for jobID inside a single transaction, so the precondition
// check and the write cannot race a concurrent caller. Reachable only
// from a controller-role context: requireControllerCtx is the same
// R-21.169 guard Scheduler.Resume and Scheduler.Advance already check, so
// a node daemon (or an un-annotated ctx, which resolves to the more
// restrictive role) refuses here even if it somehow reached this call.
func (s *Store) PutResumeTransition(ctx context.Context, jobID string, now int64) error {
	if err := requireControllerCtx(ctx, "job.resume"); err != nil {
		return err
	}
	if jobID == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: PutResumeTransition requires a job id")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		job, ok, err := getJobTx(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if !ok {
			return cascade.Newf(cascade.KindNotFound, "jobs: job %q not found", jobID)
		}
		if err := ResumeTransitionAllowed(job.State, JobStateLeased); err != nil {
			return err
		}
		blocked, err := holderHasExpiredUnconfirmedLeaseTx(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if blocked {
			return cascade.Newf(cascade.KindConflict,
				"jobs: job %q has an expired_unconfirmed lease; only the reclaim path may resume it", jobID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+tableJob+` SET state = ?, updated_at = ? WHERE id = ?`,
			string(JobStateLeased), now, jobID); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put resume transition")
		}
		return nil
	})
}

// holderHasExpiredUnconfirmedLeaseTx reports whether any lease currently
// naming holder is expired_unconfirmed -- the AT-THE-TRANSITION
// precondition check R-16.82 requires, independent of and in addition to
// reenterStaleJobs's own (upstream) check of the same condition.
func holderHasExpiredUnconfirmedLeaseTx(ctx context.Context, tx *sql.Tx, holder string) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT repo_id, scope_glob, holder, issued_at, ttl_seconds, renew_count, journal_ref, epoch, state
		 FROM `+tableResourceLease+` WHERE holder = ?`, holder)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list leases for resume precondition")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return false, err
		}
		if l.State == LeaseExpiredUnconfirmed {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: iterate leases for resume precondition")
	}
	return false, nil
}
