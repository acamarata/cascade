package learn

// Purpose: the jobs_scheduler_decisions writer. One immutable row records
//   what the Conductor decided at dispatch time (R-21.167): job, execution,
//   task class, selected tier, lane and node, the score used, the fallback
//   level it came from, whether the jump rule fired and why (the CLOSED
//   capacity.JumpReasonCode, never Explain() text) and the reserve flag.
// Inputs: a SchedulerDecision and an Execer (a *sql.DB, or a caller's *sql.Tx
//   so the lease can record inside its reservation transaction).
// Outputs: one INSERT through q. There is no UPDATE path: rows are immutable.
//   A later field set goes in a child table jobs_scheduler_decision_<name>
//   keyed by decision_id at a higher SchemaVersion, never by ALTER.
// Constraints: the id is caller-minted so the lease can return it; the id
//   and the two closed enums, a set DecidedAt, a finite [0,1] score and a
//   JumpRuleFired that matches JumpReasonCode are validated before any SQL
//   runs (KindInvalidInput);
//   no free-text column exists; job_id is stored through storedJobID so it
//   joins jobs_telemetry_outcomes exactly as the outcome writer stored it.
// SPORT: internal.learn.SchedulerDecisionWriter/ADDED,
//   internal.learn.SQLiteSchedulerDecisionWriter/ADDED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Execer is the write surface a *sql.DB and a *sql.Tx share.
type Execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// SchedulerDecision is one dispatch decision. SelectedLaneID is the lane the
// lease resolved; ExecutionID is "" outside a dispatch; SelectedNodeID is ""
// when no node was chosen.
type SchedulerDecision struct {
	ID, JobID, ExecutionID, TaskClass, SelectedTier, SelectedLaneID, SelectedNodeID string
	ScoreAtSelection                                                                float64
	FallbackLevel                                                                   FallbackLevel
	JumpRuleFired                                                                   bool
	JumpReasonCode                                                                  capacity.JumpReasonCode
	ReserveTier0                                                                    bool
	DecidedAt                                                                       time.Time
}

// SchedulerDecisionWriter records one decision through q.
type SchedulerDecisionWriter interface {
	Record(ctx context.Context, q Execer, d SchedulerDecision) error
}

// SQLiteSchedulerDecisionWriter is the production SchedulerDecisionWriter.
type SQLiteSchedulerDecisionWriter struct{}

var _ SchedulerDecisionWriter = SQLiteSchedulerDecisionWriter{}

// validateDecision runs every refusal before any SQL: id present and bounded,
// the two closed enums, and bounded id-shaped fields.
func validateDecision(q Execer, d SchedulerDecision) error {
	if q == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision Record requires an Execer")
	}
	if d.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision id must be non-empty")
	}
	if !d.FallbackLevel.Valid() {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision fallback_level is not one of the closed enum values")
	}
	if !d.JumpReasonCode.Valid() {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision jump_reason_code is not one of the closed enum values")
	}
	for _, f := range []string{d.ID, d.ExecutionID, d.TaskClass, d.SelectedTier, d.SelectedLaneID, d.SelectedNodeID} {
		if len(f) > identifierMaxLen {
			return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision field exceeds the 64-character id bound")
		}
	}
	return validateDecisionValues(d)
}

// validateDecisionValues refuses the numeric and consistency faults: a zero
// DecidedAt (it would store year 1), a score that is not a finite number in
// [0,1], and a JumpRuleFired flag that disagrees with JumpReasonCode (the
// rule fired exactly when the code is not "none").
func validateDecisionValues(d SchedulerDecision) error {
	if d.DecidedAt.IsZero() {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision decided_at must be set")
	}
	if math.IsNaN(d.ScoreAtSelection) || math.IsInf(d.ScoreAtSelection, 0) || d.ScoreAtSelection < 0 || d.ScoreAtSelection > 1 {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision score_at_selection must be a finite number in [0,1]")
	}
	if d.JumpRuleFired != (d.JumpReasonCode != capacity.JumpReasonNone) {
		return cascade.New(cascade.KindInvalidInput, "learn: scheduler decision jump_rule_fired disagrees with jump_reason_code")
	}
	return nil
}

// Record inserts d as one jobs_scheduler_decisions row through q. A repeated
// id is KindConflict (rows are immutable; nothing is overwritten).
func (SQLiteSchedulerDecisionWriter) Record(ctx context.Context, q Execer, d SchedulerDecision) error {
	if err := validateDecision(q, d); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO `+tableSchedulerDecision+`
		(id, job_id, execution_id, task_class, selected_tier, selected_lane_id, selected_node_id,
		 score_at_selection, fallback_level, jump_rule_fired, jump_reason_code, reserve_tier0_flag, decided_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, storedJobID(d.JobID), d.ExecutionID, d.TaskClass, d.SelectedTier, d.SelectedLaneID, d.SelectedNodeID,
		d.ScoreAtSelection, string(d.FallbackLevel), boolToInt(d.JumpRuleFired), string(d.JumpReasonCode),
		boolToInt(d.ReserveTier0), d.DecidedAt.UTC().Unix())
	if err != nil {
		return classifyDecisionInsertErr(err)
	}
	return nil
}

// classifyDecisionInsertErr maps a duplicate primary key to KindConflict and
// every other failure to KindUnavailable. The driver text is matched, not
// echoed: the wrapped error keeps it, the message names no value.
func classifyDecisionInsertErr(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "PRIMARY KEY") {
		return cascade.Wrap(cascade.KindConflict, err, "learn: scheduler decision id already recorded (rows are immutable)")
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert jobs_scheduler_decisions row")
}
