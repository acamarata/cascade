package economics

// Purpose: TTL-bounded per-project reserve overrides with a rolling weekly cap.
// Inputs: the scheduler mode database and its injected Clock.
// Outputs: explicit grants or fail-closed errors using the existing taxonomy.
// Constraints: clearing a grant preserves history for cap enforcement.
// SPORT: fleet/economics/reserve-override/ADD (P1-TOP-20).

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const reserveOverrideTTL = 4 * time.Hour
const reserveOverrideWindow = 7 * 24 * time.Hour
const reserveOverrideCap = 3

// ErrReserveOverrideTTL refuses a missing, negative, or excessive override TTL.
var ErrReserveOverrideTTL = cascade.New(cascade.KindInvalidInput, "economics: reserve override ttl must be positive and at most 4h")

// ErrReserveOverrideCap refuses a fourth reserve grant in a rolling week.
var ErrReserveOverrideCap = cascade.New(cascade.KindConflict, "economics: reserve override cap reached for this project this week")

// ApprovalIDReserveOverrideCap identifies the approval required at the grant cap.
const ApprovalIDReserveOverrideCap = "I-S18-T3-reserve-override-cap"

// AllowReserve grants projectID reserve access for a positive TTL of at most 4h.
// At most three grants succeed in a rolling week, including cleared grants.
func (s *SchedulerModeStore) AllowReserve(ctx context.Context, projectID string, ttl time.Duration) error {
	if projectID == "" {
		return cascade.New(cascade.KindInvalidInput, "economics: AllowReserve requires a non-empty project_id")
	}
	if ttl <= 0 || ttl > reserveOverrideTTL {
		return ErrReserveOverrideTTL
	}
	now := s.clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: begin reserve override")
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO `+tableReserveOverride+`
(project_id, set_at, expires_at, cleared) SELECT ?, ?, ?, 0
WHERE (SELECT COUNT(*) FROM `+tableReserveOverride+` WHERE project_id = ? AND set_at > ?) < ?`,
		projectID, now.UnixNano(), now.Add(ttl).UnixNano(), projectID, now.Add(-reserveOverrideWindow).UnixNano(), reserveOverrideCap)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: insert reserve override")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: reserve override affected rows")
	}
	if n == 0 {
		if err := recordReserveApproval(ctx, tx, projectID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: commit reserve override")
	}
	if n == 0 {
		return ErrReserveOverrideCap
	}
	return nil
}

// recordReserveApproval journals a refused grant within its transaction.
func recordReserveApproval(ctx context.Context, tx *sql.Tx, projectID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+tableSchedulerModeJournal+`
(project_id, event, approval_id, recorded_at) VALUES (?, ?, ?, ?)`,
		projectID, EventHumanApprovalRequired, ApprovalIDReserveOverrideCap, now.Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: record reserve override journal")
	}
	return nil
}

// ReserveAllowed reports whether projectID's latest grant is live and uncleared.
// Reads never mutate history, and every read failure returns false and an error.
func (s *SchedulerModeStore) ReserveAllowed(ctx context.Context, projectID string) (bool, error) {
	if projectID == "" {
		return false, cascade.New(cascade.KindInvalidInput, "economics: ReserveAllowed requires a non-empty project_id")
	}
	now := s.clock.Now().UnixNano()
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT
cleared = 0 AND set_at <= ? AND expires_at > ? FROM `+tableReserveOverride+`
WHERE project_id = ? ORDER BY id DESC LIMIT 1), 0)`, now, now, projectID).Scan(&allowed)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "economics: select reserve override")
	}
	return allowed, nil
}

// ClearReserve revokes projectID's grants without resetting the weekly cap.
func (s *SchedulerModeStore) ClearReserve(ctx context.Context, projectID string) error {
	if projectID == "" {
		return cascade.New(cascade.KindInvalidInput, "economics: ClearReserve requires a non-empty project_id")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableReserveOverride+` SET cleared = 1 WHERE project_id = ? AND cleared = 0`, projectID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "economics: clear reserve override")
	}
	return nil
}
