package jobs

// Purpose: HOW 3's re-entry scan (scheduler_resume.go's funlen/300-line
//
//	split): find every `running` job whose lease has gone stale and move
//	it back to `leased`, applying the transition through the R-16.82
//	restricted edge rather than only announcing it.
//
// Inputs: an open, migrated *Store, a Reader wired to the real journal,
//
//	and Resume's own `now`.
//
// Outputs: the re-entry Events for the coordinator to feed into Advance,
//
//	or a typed error.
//
// Constraints: a job whose lease is expired_unconfirmed is left alone
//
//	(R-21.139/R-21.177 -- only the S-59.T2 reclaim path may move it); a
//	job with no journal trail at all is not re-entered.
//
// SPORT: jobs/scheduler/ADD (P1-E29-W6-S59-T5); FIX R-16.82.

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// reenterStaleJobs implements HOW 3's re-entry scan: a `running` job
// whose lease has passed ttl+expiry_grace RE-ENTERS `leased` (the
// coordinator's next Advance call then moves it leased->running per
// R-16.68c); a job within the grace window, or whose lease is
// expired_unconfirmed, is left alone. Per R-16.82, the transition is
// APPLIED here (PutResumeTransition), not merely announced by the
// returned event.
func (s *Scheduler) reenterStaleJobs(ctx context.Context, store *Store, journal Reader, now int64) ([]Event, error) {
	staleness := DefaultLeaseDefaults()
	threshold := staleness.TTLSeconds + staleness.ExpiryGraceSeconds

	runningJobIDs, err := runningJobIDs(ctx, store.db)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, jobID := range runningJobIDs {
		leases, err := store.allLeasesForHolder(ctx, jobID)
		if err != nil {
			return nil, err
		}
		lease, ok := staleHolderLease(leases, now, threshold)
		if !ok {
			continue
		}
		if lease.State == LeaseExpiredUnconfirmed {
			continue // R-21.139/R-21.177: wait for the reclaim path
		}
		// Re-entry is only trusted for a job the REAL M/S-27.T1 journal
		// actually recorded progress for -- an on-disk jobs_job row with
		// no journal trail at all is not this ticket's re-entry case to
		// guess at (HOW 3's "journal replay" requirement; the kill -9
		// test proves this path is genuinely exercised, not a dead
		// parameter).
		if journal != nil {
			entries, err := journal.Replay(ctx, jobID, 0)
			if err != nil {
				return nil, err
			}
			if len(entries) == 0 {
				continue
			}
		}
		// APPLY the transition, not merely announce it (R-16.82 /
		// DEFECT-resume-transition-not-applied): PutResumeTransition
		// writes the job's stored state through the resume-only
		// restricted edge, re-checking the expired_unconfirmed
		// precondition at the transition itself.
		if err := store.PutResumeTransition(ctx, jobID, now); err != nil {
			return nil, err
		}
		events = append(events, JobTransitioned{JobID: jobID, From: JobStateRunning, To: JobStateLeased})
	}
	return events, nil
}

// staleHolderLease returns the first of leases whose issued+ttl+grace
// has elapsed, if any.
func staleHolderLease(leases []ResourceLease, now, thresholdSeconds int64) (ResourceLease, bool) {
	for _, l := range leases {
		if now-l.IssuedAt >= thresholdSeconds {
			return l, true
		}
	}
	return ResourceLease{}, false
}

func runningJobIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM `+tableJob+` WHERE state = ?`, string(JobStateRunning))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: list running jobs")
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: scan running job id")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scheduler: iterate running jobs")
	}
	return ids, nil
}
