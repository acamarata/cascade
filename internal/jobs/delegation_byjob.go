package jobs

// Purpose: the job-ID lookup over the delegation record
// (contract:delegation-record): CI result routing (P1-CI-06) resolves a
// finished job back to the delegation that launched it.
//
// SPORT: jobs/delegation-record/ADD (P1-CI-06a).

import "context"

// DelegationByJobID returns the newest record (by CreatedAt, then insert
// order) whose JobID is jobID, in any state; ok is false when there is
// none. An empty jobID is KindInvalidInput.
func (s *Store) DelegationByJobID(ctx context.Context, jobID string) (DelegationRecord, bool, error) {
	if jobID == "" {
		return DelegationRecord{}, false, invalidDelegation("a job ID is required")
	}
	return scanDelegation(s.db.QueryRowContext(ctx,
		`SELECT `+delegationCols+` FROM `+tableDelegation+
			` WHERE job_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, jobID))
}
