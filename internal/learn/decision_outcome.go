package learn

// Purpose: the learn SchemaVersion 3 child tables of jobs_scheduler_decisions
//   and the insert-once outcome writer. Economics and outcome fields live in
//   tables keyed by decision_id because a decision row is immutable and the
//   DSL has no ALTER: a decision recorded at version 2 simply has no child row.
// Inputs: a decision id, an OutcomeKind and an injected Clock.
// Outputs: the version-3 migration steps; ParseOutcomeKind, the closed-set
//   decoder; InsertDecisionOutcome, which reports the rows it changed.
// Constraints: every refusal runs before any SQL. An outcome row is written
//   once: a repeat of the stored kind changes 0 rows, a different kind is
//   ErrOutcomeKindConflict and a decision that was never recorded is
//   ErrDecisionNotRecorded. No free-text column exists on either table.
// SPORT: domain:jobs/jobs_scheduler_decision_economics,
//   jobs_scheduler_decision_outcome — new tables under SetID learn v3
//   (P1-TOP-10); internal.learn.OutcomeKind/ADDED,
//   internal.learn.ParseOutcomeKind/ADDED,
//   internal.learn.InsertDecisionOutcome/ADDED (P1-TOP-10).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names the version-3 steps create (jobs_ prefix, same domain).
const (
	tableDecisionEconomics = "jobs_scheduler_decision_economics"
	tableDecisionOutcome   = "jobs_scheduler_decision_outcome"
)

// OutcomeKind is the closed set of stored outcomes for one decision (P1-TOP-10
// contract). A decision with no row is pending. completed, failed and cancelled
// equal jobs.TerminalOutcome's values; expired is written only by the orphan sweep.
type OutcomeKind string

// The closed outcome kinds.
const (
	OutcomeKindCompleted OutcomeKind = "completed"
	OutcomeKindFailed    OutcomeKind = "failed"
	OutcomeKindCancelled OutcomeKind = "cancelled"
	OutcomeKindExpired   OutcomeKind = "expired"
)

// The sentinels callers compare by identity. Each message names no value.
var (
	// ErrUnknownOutcomeKind refuses a kind outside the closed set.
	ErrUnknownOutcomeKind error = cascade.New(cascade.KindInvalidInput,
		"learn: outcome_kind is not one of the closed enum values")
	// ErrOutcomeKindConflict refuses a kind that differs from the stored one.
	ErrOutcomeKindConflict error = cascade.New(cascade.KindConflict,
		"learn: decision outcome already recorded with a different outcome_kind")
	// ErrDecisionNotRecorded refuses an outcome for an absent decision row.
	ErrDecisionNotRecorded error = cascade.New(cascade.KindNotFound,
		"learn: decision outcome names no recorded scheduler decision")
)

// ParseOutcomeKind fail-closed decodes raw: unrecognised is
// ErrUnknownOutcomeKind and the zero OutcomeKind.
func ParseOutcomeKind(raw string) (OutcomeKind, error) {
	switch OutcomeKind(raw) {
	case OutcomeKindCompleted, OutcomeKindFailed, OutcomeKindCancelled, OutcomeKindExpired:
		return OutcomeKind(raw), nil
	default:
		return "", ErrUnknownOutcomeKind
	}
}

// DecisionOutcomeStore is the surface a *sql.DB and a *sql.Tx share that
// InsertDecisionOutcome needs: the insert, then the read of an existing row.
type DecisionOutcomeStore interface {
	Execer
	queryRower
}

// InsertDecisionOutcome records kind for decisionID at clock's time and
// returns the rows changed: 1 for the first insert, 0 for a repeat of the
// stored kind. The stored row is never rewritten.
func InsertDecisionOutcome(ctx context.Context, q DecisionOutcomeStore, clock migrate.Clock, decisionID string, kind OutcomeKind) (int64, error) {
	if q == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: InsertDecisionOutcome requires a store")
	}
	if clock == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: InsertDecisionOutcome requires a non-nil Clock")
	}
	if decisionID == "" || len(decisionID) > identifierMaxLen {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: decision outcome id must be non-empty and within the 64-character id bound")
	}
	if _, err := ParseOutcomeKind(string(kind)); err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx, `INSERT INTO `+tableDecisionOutcome+` (decision_id, outcome_kind, recorded_at)
		SELECT id, ?, ? FROM `+tableSchedulerDecision+` WHERE id = ?
		ON CONFLICT (decision_id) DO NOTHING`, string(kind), clock.Now().UTC().Unix(), decisionID)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: insert jobs_scheduler_decision_outcome row")
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: read jobs_scheduler_decision_outcome insert result")
	}
	if changed != 0 {
		return changed, nil
	}
	return 0, checkStoredOutcome(ctx, q, decisionID, kind)
}

// checkStoredOutcome explains an insert that changed nothing: a matching row
// is a clean repeat, a differing row a conflict, no row an absent decision.
func checkStoredOutcome(ctx context.Context, q queryRower, decisionID string, kind OutcomeKind) error {
	var stored string
	err := q.QueryRowContext(ctx, `SELECT outcome_kind FROM `+tableDecisionOutcome+` WHERE decision_id = ?`, decisionID).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrDecisionNotRecorded
	case err != nil:
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: read jobs_scheduler_decision_outcome row")
	case stored != string(kind):
		return ErrOutcomeKindConflict
	}
	return nil
}

// learnStepsV3 is the P1-TOP-10 step list: both child tables, each with one
// row per decision. The steps of every earlier version stay first and unchanged.
func learnStepsV3() []migrate.MigrationStep {
	return []migrate.MigrationStep{decisionEconomicsTableStep(), decisionOutcomeTableStep()}
}

// decisionEconomicsTableStep: the numbers behind one decision. regret is
// nullable because it is only known after the outcome.
func decisionEconomicsTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_scheduler_decision_economics: numeric economics of one scheduler decision",
		Table: &migrate.TableDef{
			Name: tableDecisionEconomics,
			Columns: []migrate.ColumnDef{
				{Name: "decision_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "shadow_price", Type: migrate.TypeReal, NotNull: true},
				{Name: "correlation", Type: migrate.TypeReal, NotNull: true},
				{Name: "utility", Type: migrate.TypeReal, NotNull: true},
				{Name: "predicted_success", Type: migrate.TypeReal, NotNull: true},
				{Name: "regret", Type: migrate.TypeReal},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "decision_id", RefTable: tableSchedulerDecision, RefColumn: "id"},
			},
		},
	}
}

// decisionOutcomeTableStep: the closed outcome of one decision, written once.
func decisionOutcomeTableStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "jobs_scheduler_decision_outcome: closed outcome kind of one scheduler decision",
		Table: &migrate.TableDef{
			Name: tableDecisionOutcome,
			Columns: []migrate.ColumnDef{
				{Name: "decision_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
				{Name: "outcome_kind", Type: migrate.TypeText, NotNull: true},
				{Name: "recorded_at", Type: migrate.TypeInteger, NotNull: true},
			},
			ForeignKeys: []migrate.ForeignKeyDef{
				{Column: "decision_id", RefTable: tableSchedulerDecision, RefColumn: "id"},
			},
		},
	}
}
