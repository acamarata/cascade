package jobs

// Purpose: persist completion-gate denial counts for each execution.
// Inputs: the jobs store, job id and verbatim denial text.
// Outputs: durable attempt counts and the shared bounded retry policy.
// Constraints: one atomic write allocates each attempt; no job state changes.
// SPORT: jobs/completion-gate/ADD (P1-PBD-04).

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// GateAttemptLimit is the number of denials before a gate must stall.
const GateAttemptLimit = 3

// ErrGateAttemptOutOfRange identifies attempts outside the retry policy.
var ErrGateAttemptOutOfRange = cascade.New(cascade.KindInvalidInput, "jobs: gate attempt out of range")

// GateAttemptPolicy defines the completion gate's shared retry bound.
type GateAttemptPolicy struct{}

// Delay returns the backoff for a one-based denial attempt, or the range
// sentinel when the attempt falls outside the three-attempt policy.
func (GateAttemptPolicy) Delay(attempt int) (time.Duration, error) {
	if attempt < 1 || attempt > GateAttemptLimit {
		return 0, ErrGateAttemptOutOfRange
	}
	return [...]time.Duration{5 * time.Minute, 15 * time.Minute, 45 * time.Minute}[attempt-1], nil
}

// ShouldStall reports whether the denial count has exhausted the bound.
func (GateAttemptPolicy) ShouldStall(attempt int) bool {
	return attempt >= GateAttemptLimit
}

// RecordGateDenial records reason verbatim for the job's latest execution and
// returns its new denial count. A new execution starts at one. Counts remain
// monotonic beyond the retry bound so callers can always detect exhaustion.
func (s *Store) RecordGateDenial(ctx context.Context, jobID, reason string) (int, error) {
	if s == nil || s.db == nil || jobID == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "jobs: RecordGateDenial requires a store and job id")
	}
	var attempt int
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableGateAttempts+` (execution_id, attempts, reason)
		SELECT id, 1, ? FROM `+tableExecution+` WHERE job_id = ?
		ORDER BY attempt DESC, started_at DESC, id DESC LIMIT 1
		ON CONFLICT(execution_id) DO UPDATE SET attempts = attempts + 1, reason = excluded.reason
		RETURNING attempts`, reason, jobID).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, cascade.New(cascade.KindNotFound, "jobs: gate denial requires an execution")
	}
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "jobs: record gate denial")
	}
	return attempt, nil
}
