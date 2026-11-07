package learn

// Purpose: crash repair and the orphan sweep for decision outcomes. A job that
//   committed a terminal state but crashed before the observer wrote its
//   outcome gets the mapped row from ReconcileTerminalOutcomes; a decision
//   with no outcome whose execution is no longer live is expired by
//   SweepOrphanedPending. Startup order: reconcile, then sweep.
// Inputs: a *sql.DB, an injected Clock, and injected state and liveness funcs.
// Outputs: the number of outcome rows inserted; both calls are idempotent.
// Constraints: every write goes through InsertDecisionOutcome. A state or
//   liveness error is returned and never read as absence, so no outcome is
//   invented on an error. An existing outcome row is never rewritten.
// SPORT: internal.learn.TerminalJob/ADDED,
//   internal.learn.ReconcileTerminalOutcomes/ADDED,
//   internal.learn.SweepOrphanedPending/ADDED (P1-TOP-10).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TerminalJob is one job the authoritative job state reports as terminal.
// State is the stored job state: accepted, rejected, failed or cancelled.
type TerminalJob struct{ JobID, State string }

// terminalOutcomeKind maps a terminal job state to its stored outcome.
func terminalOutcomeKind(state string) (OutcomeKind, error) {
	switch state {
	case "accepted":
		return OutcomeKindCompleted, nil
	case "rejected", "failed":
		return OutcomeKindFailed, nil
	case "cancelled":
		return OutcomeKindCancelled, nil
	}
	return "", cascade.New(cascade.KindInvalidInput, "learn: terminal job state is not a known terminal state")
}

// pendingDecision is a decision with no outcome row.
type pendingDecision struct{ id, executionID string }

// pendingDecisions lists the decisions of jobID (every job when jobID is
// empty) that have no outcome row, in id order. The rows are closed before
// the caller writes.
func pendingDecisions(ctx context.Context, db *sql.DB, jobID string) ([]pendingDecision, error) {
	query := `SELECT d.id, d.execution_id FROM ` + tableSchedulerDecision + ` d
		LEFT JOIN ` + tableDecisionOutcome + ` o ON o.decision_id = d.id
		WHERE o.decision_id IS NULL`
	var args []any
	if jobID != "" {
		query += ` AND d.job_id = ?`
		args = append(args, jobID)
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY d.id`, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read decisions without an outcome")
	}
	defer func() { _ = rows.Close() }()
	var out []pendingDecision
	for rows.Next() {
		var p pendingDecision
		if err := rows.Scan(&p.id, &p.executionID); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan decision without an outcome")
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate decisions without an outcome")
	}
	return out, nil
}

// ReconcileTerminalOutcomes inserts the mapped outcome for every decision of a
// terminal job that has none, and returns the rows inserted. An unknown state
// is KindInvalidInput with nothing written; a terminal read error is returned.
func ReconcileTerminalOutcomes(ctx context.Context, db *sql.DB, clock migrate.Clock, terminal func(context.Context) ([]TerminalJob, error)) (int, error) {
	if db == nil || clock == nil || terminal == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: ReconcileTerminalOutcomes requires a database, a Clock and a terminal reader")
	}
	jobs, err := terminal(ctx)
	if err != nil {
		return 0, err
	}
	kinds := make([]OutcomeKind, len(jobs))
	for i, j := range jobs {
		if kinds[i], err = terminalOutcomeKind(j.State); err != nil {
			return 0, err
		}
	}
	inserted := 0
	for i, j := range jobs {
		if j.JobID == "" {
			continue
		}
		pending, err := pendingDecisions(ctx, db, j.JobID)
		if err != nil {
			return inserted, err
		}
		for _, p := range pending {
			n, err := InsertDecisionOutcome(ctx, db, clock, p.id, kinds[i])
			if err != nil {
				return inserted, err
			}
			inserted += int(n)
		}
	}
	return inserted, nil
}

// SweepOrphanedPending inserts 'expired' for every decision with no outcome
// whose execution is not live, and returns the rows inserted. A liveness error
// is returned with nothing written for that decision.
func SweepOrphanedPending(ctx context.Context, db *sql.DB, clock migrate.Clock, live func(executionID string) (bool, error)) (int, error) {
	if db == nil || clock == nil || live == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "learn: SweepOrphanedPending requires a database, a Clock and a liveness func")
	}
	pending, err := pendingDecisions(ctx, db, "")
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, p := range pending {
		isLive, err := live(p.executionID)
		if err != nil {
			return inserted, err
		}
		if isLive {
			continue
		}
		n, err := InsertDecisionOutcome(ctx, db, clock, p.id, OutcomeKindExpired)
		if err != nil {
			return inserted, err
		}
		inserted += int(n)
	}
	return inserted, nil
}
