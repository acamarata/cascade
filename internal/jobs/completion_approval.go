package jobs

// Purpose: typed CRUD for jobs_completion_approval (P1-PBD-09): one held
//
//	completion-approval row per (job, action). Put is insert-once (a
//	second Put on the same key leaves the stored row unchanged), For
//	reads it, MarkDenied flips Denied, Delete removes it.
//
// Inputs: an open *sql.DB migrated via ApplyCompletionApprovalSchema.
// Outputs: typed errors; KindNotFound when MarkDenied finds no row.
//
// Constraints: the nonce never leaves this store through an error
// message; callers own every log, event and RPC surface.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CompletionApprovalPolicyVersion is the policy version stamped on every
// completion-approval row this build writes.
const CompletionApprovalPolicyVersion = "completion-approval-v1"

// ErrApprovalPending is returned while a completion waits on its held
// approval row.
var ErrApprovalPending = cascade.New(cascade.KindElevationRequired, "jobs: completion approval is pending")

// CompletionApproval is one jobs_completion_approval row.
type CompletionApproval struct {
	// JobID and Action form the row's natural key.
	JobID, Action string
	// Nonce binds the approval to one request; never logged.
	Nonce string
	// PolicyVersion is the policy version the row was written under.
	PolicyVersion string
	// Denied is true once the approval was refused.
	Denied bool
	// CreatedAt is the injected clock's Unix time at Put.
	CreatedAt int64
}

// PutCompletionApproval inserts a row keyed by (JobID, Action). A row
// already stored on that key is left unchanged.
func (s *Store) PutCompletionApproval(ctx context.Context, a CompletionApproval) error {
	if a.JobID == "" || a.Action == "" || a.Nonce == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: completion approval job_id, action and nonce are required")
	}
	if a.PolicyVersion == "" {
		a.PolicyVersion = CompletionApprovalPolicyVersion
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableCompletionApproval+` (job_id, action, nonce, policy_version, denied, created_at)
		 VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(job_id, action) DO NOTHING`,
		a.JobID, a.Action, a.Nonce, a.PolicyVersion, boolToInt(a.Denied), a.CreatedAt)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: put completion approval")
	}
	return nil
}

// CompletionApprovalFor reads the row for (jobID, action). An absent row
// returns found false and a nil error.
func (s *Store) CompletionApprovalFor(ctx context.Context, jobID, action string) (CompletionApproval, bool, error) {
	var a CompletionApproval
	var denied int64
	err := s.db.QueryRowContext(ctx,
		`SELECT job_id, action, nonce, policy_version, denied, created_at
		 FROM `+tableCompletionApproval+` WHERE job_id = ? AND action = ?`, jobID, action).
		Scan(&a.JobID, &a.Action, &a.Nonce, &a.PolicyVersion, &denied, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionApproval{}, false, nil
	}
	if err != nil {
		return CompletionApproval{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: read completion approval")
	}
	a.Denied = denied != 0
	return a, true, nil
}

// MarkCompletionApprovalDenied sets Denied on the (jobID, action) row.
// An absent row is KindNotFound.
func (s *Store) MarkCompletionApprovalDenied(ctx context.Context, jobID, action string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE `+tableCompletionApproval+` SET denied = 1 WHERE job_id = ? AND action = ?`, jobID, action)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: deny completion approval")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: deny completion approval")
	}
	if n == 0 {
		return cascade.New(cascade.KindNotFound, "jobs: no completion approval for that job and action")
	}
	return nil
}

// DeleteCompletionApproval removes the (jobID, action) row. Deleting an
// absent row is not an error.
func (s *Store) DeleteCompletionApproval(ctx context.Context, jobID, action string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+tableCompletionApproval+` WHERE job_id = ? AND action = ?`, jobID, action)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: delete completion approval")
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
