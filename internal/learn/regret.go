package learn

// Purpose: the write-once regret of a scheduler decision and the aggregate
//   over decisions that ended completed or failed.
// Inputs: a job id and the best-in-hindsight utility; a query surface.
// Outputs: RegretFiller.Fill, which stores best - utility once; ErrDecisionNotFound;
//   RegretAggregate, the count, total and mean of the stored regrets.
// Constraints: utility is read from the economics row, never recomputed. A
//   stored regret is never rewritten and never clamped (negative is valid).
//   The aggregate reads only completed and failed outcomes. The regret column
//   lives on jobs_scheduler_decision_economics (nullable until filled).
// SPORT: internal.learn.RegretFiller/ADDED, internal.learn.ErrDecisionNotFound/ADDED,
//   internal.learn.RegretAggregate/ADDED (P1-TOP-10C).

import (
	"context"
	"database/sql"
	"math"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrDecisionNotFound refuses a regret for a job with no economics row.
var ErrDecisionNotFound error = cascade.New(cascade.KindNotFound,
	"learn: regret names no recorded scheduler decision economics")

// RegretFiller stores regret through DB.
type RegretFiller struct {
	DB interface {
		Execer
		queryRower
	}
}

// Fill stores bestInHindsightUtility - utility for every unfilled economics
// row of jobID. A job whose regret is already stored is a no-op returning nil.
func (f *RegretFiller) Fill(ctx context.Context, jobID string, bestInHindsightUtility float64) error {
	if f == nil || f.DB == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: RegretFiller requires a store")
	}
	if jobID == "" || len(jobID) > identifierMaxLen {
		return cascade.New(cascade.KindInvalidInput, "learn: regret job id must be non-empty and within the 64-character id bound")
	}
	if math.IsNaN(bestInHindsightUtility) || math.IsInf(bestInHindsightUtility, 0) {
		return cascade.New(cascade.KindInvalidInput, "learn: best-in-hindsight utility must be finite")
	}
	var known int
	err := f.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableDecisionEconomics+` e
		JOIN `+tableSchedulerDecision+` d ON d.id = e.decision_id WHERE d.job_id = ?`, jobID).Scan(&known)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: read jobs_scheduler_decision_economics rows")
	}
	if known == 0 {
		return ErrDecisionNotFound
	}
	if _, err := f.DB.ExecContext(ctx, `UPDATE `+tableDecisionEconomics+` SET regret = ? - utility
		WHERE regret IS NULL AND decision_id IN (SELECT id FROM `+tableSchedulerDecision+` WHERE job_id = ?)`,
		bestInHindsightUtility, jobID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: store decision regret")
	}
	return nil
}

// RegretSummary is the aggregate over filled regrets.
type RegretSummary struct {
	Count      int
	Total, Avg float64
}

// RegretAggregate sums the stored regrets of decisions whose outcome is
// completed or failed. Cancelled, expired and pending decisions are ignored.
func RegretAggregate(ctx context.Context, q queryRower) (RegretSummary, error) {
	if q == nil {
		return RegretSummary{}, cascade.New(cascade.KindInvalidInput, "learn: RegretAggregate requires a store")
	}
	var s RegretSummary
	var total sql.NullFloat64
	err := q.QueryRowContext(ctx, `SELECT COUNT(e.regret), SUM(e.regret) FROM `+tableDecisionEconomics+` e
		JOIN `+tableDecisionOutcome+` o ON o.decision_id = e.decision_id
		WHERE e.regret IS NOT NULL AND o.outcome_kind IN (?, ?)`,
		string(OutcomeKindCompleted), string(OutcomeKindFailed)).Scan(&s.Count, &total)
	if err != nil {
		return RegretSummary{}, cascade.Wrap(cascade.KindUnavailable, err, "learn: read regret aggregate")
	}
	if s.Count > 0 {
		s.Total = total.Float64
		s.Avg = s.Total / float64(s.Count)
	}
	return s, nil
}
