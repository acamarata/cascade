// Purpose: the rows Lease writes inside the Bind transaction beside the
// reservation's placement: the execution row (only when the dispatch names a
// job) and the scheduler decision, recorded through the DecisionRecorder port
// (capacity never imports internal/learn).
//
// Inputs: a leasePlan and the transaction Bind opened.
// Outputs: a BindFn the Reserver runs on that transaction.
// Constraints: both writes use the Bind tx and nothing else: a read or write
// on any other handle would wait for the transaction's only connection. An
// error from either rolls the whole lease back, so a placement never exists
// without the rows that explain it.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// bindFn returns the BindFn that writes p's execution row and decision row.
func (f *Fleet) bindFn(p leasePlan) economics.BindFn {
	return func(ctx context.Context, tx *sql.Tx, _ economics.Reservation) error {
		if p.d.JobID != "" {
			if err := f.recheckFloor(ctx, tx, p); err != nil {
				return err
			}
			e := jobs.Execution{ID: p.ids.execution, JobID: p.d.JobID, Attempt: p.attempt, State: jobs.ExecutionRunning}
			if err := f.deps.Jobs.PutExecutionTx(ctx, tx, e); err != nil {
				return err
			}
		}
		return f.deps.Decisions.Record(ctx, tx, f.decisionRecord(p))
	}
}

// recheckFloor re-reads the job's data class inside the Bind transaction. The
// plan validated the floor before Reserve; a class raised since then must not
// let the stale tier authorise the execution.
func (f *Fleet) recheckFloor(ctx context.Context, tx *sql.Tx, p leasePlan) error {
	class, ok, err := f.deps.Jobs.JobDataClassTx(ctx, tx, p.d.JobID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "capacity: job %q does not exist", p.d.JobID)
	}
	if provider.JoinSensitivity(p.tier, class) != p.tier {
		return refuse(ErrDataClassDenied, nil, "job %q is now %s, stricter than the %s this lease was planned for", p.d.JobID, class, p.tier)
	}
	return nil
}

// decisionRecord is the scheduler decision for p: the TierSelection fields
// (tier, jump fired, reason code, reserve flag), the lane, the node and the
// score read at selection.
func (f *Fleet) decisionRecord(p leasePlan) DecisionRecord {
	return DecisionRecord{
		ID: p.ids.decision, JobID: p.d.JobID, ExecutionID: p.ids.execution, TaskClass: p.req.TaskClass,
		SelectedTier: p.sel.Tier.Tier, LaneID: p.sel.Lane, NodeID: p.node, ScoreAtSelection: p.sel.Score,
		FallbackLevel: fallbackLevelGlobal, JumpRuleFired: p.sel.Tier.JumpTriggered,
		JumpReasonCode: p.sel.Tier.JumpReasonCode, ReserveTier0: p.sel.Tier.ReserveApplied, DecidedAt: f.deps.Clock.Now(),
	}
}
