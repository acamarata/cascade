package learn

// Purpose: OutcomeReconciler -- the real production driver of
//   SQLiteOutcomeWriter.Record. R-14.316: terminal job transitions are
//   committed by more than one path (CompletionPolicy.commit,
//   jobs.(*Store).PutTransition's terminal hook, the scheduler's
//   cancelling->cancelled advance), so no single hook observes all of
//   them. This reconciler instead lists every job in each terminal
//   JobState and records an outcome for any that has none yet -- Record's
//   own ON CONFLICT(job_id) DO NOTHING makes a second pass over an
//   already-recorded job a true no-op.
// Inputs: a *jobs.Store (already migrated) and an OutcomeWriter.
// Outputs: exactly one jobs_telemetry_outcomes row per terminal job,
//   whichever path committed its transition (a job whose id is
//   credential-shaped is refused by the writer and counted instead).
// Constraints: pages via jobs.Store.ListJobs' own cursor (never loads the
//   whole table at once). One bad input never fails the whole pass:
//   labels and scope are mapped to storable values here and the writer
//   maps an unstorable job id itself, so the only per-job refusal left is a
//   credential-shaped job id. Such a job is skipped and counted, every
//   other terminal job is still recorded, and the pass then returns a
//   KindInvalidInput error carrying the count only (never silent). Any
//   other error (the store or the db) stops the pass at once.
// SPORT: internal.learn.OutcomeReconciler/ADDED (P1-E31-W6-S64-T1).

import (
	"context"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// terminalStates is the real JobState set a completed job resolves to
// (R-14.316) -- accepted/rejected map 1:1 to OutcomeClass; cancelled and
// failed both map to OutcomeUnknown.
var terminalStates = []jobs.JobState{
	jobs.JobStateAccepted, jobs.JobStateRejected, jobs.JobStateCancelled, jobs.JobStateFailed,
}

// outcomeClassFor maps a terminal JobState to R-14.316's OutcomeClass. The
// switch is exhaustive over the full JobState set (not just terminalStates)
// per the exhaustive linter -- reconcileState only ever calls this with a
// terminal state, but a non-terminal case reaching here (a future caller
// error) still resolves to the same safe OutcomeUnknown as cancelled/failed,
// never a panic or a fabricated accepted/rejected.
func outcomeClassFor(state jobs.JobState) OutcomeClass {
	switch state {
	case jobs.JobStateAccepted:
		return OutcomeAccepted
	case jobs.JobStateRejected:
		return OutcomeRejected
	case jobs.JobStateCancelled, jobs.JobStateFailed,
		jobs.JobStatePending, jobs.JobStateLeased, jobs.JobStateRunning,
		jobs.JobStateVerifying, jobs.JobStateReviewing, jobs.JobStateCancelling:
		return OutcomeUnknown
	default:
		return OutcomeUnknown
	}
}

// reconcilePageLimit bounds one ListJobs page per (state, cursor) call --
// matching jobs.Store's own maxListLimit ceiling.
const reconcilePageLimit = 500

// OutcomeReconciler is the learn-outcome-reconcile runnable's real
// implementation.
type OutcomeReconciler struct {
	Store  *jobs.Store
	Writer OutcomeWriter
}

// Reconcile walks every terminal JobState, paging by cursor, and calls
// Writer.Record for every job in that state -- idempotent by job_id, so a
// job already recorded on a prior pass is skipped as a no-op by the
// writer itself, not by this loop re-checking existence. A job the writer
// refuses (KindInvalidInput) is skipped and counted; the pass finishes
// and then reports the count.
func (rec OutcomeReconciler) Reconcile(ctx context.Context) error {
	refused := 0
	for _, state := range terminalStates {
		n, err := rec.reconcileState(ctx, state)
		refused += n
		if err != nil {
			return err
		}
	}
	if refused > 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"learn: reconcile recorded every other terminal job but the writer refused %d", refused)
	}
	return nil
}

// reconcileState pages through one terminal state's jobs and returns how
// many the writer refused, split from Reconcile for the 50-line cap.
func (rec OutcomeReconciler) reconcileState(ctx context.Context, state jobs.JobState) (int, error) {
	cursor, refused := "", 0
	for {
		page, next, err := rec.Store.ListJobs(ctx, jobs.JobFilter{State: state, Limit: reconcilePageLimit, Cursor: cursor})
		if err != nil {
			return refused, err
		}
		for _, j := range page {
			err := rec.Writer.Record(ctx, outcomeFromJob(j, state))
			switch {
			case err == nil:
			case cascade.HasKind(err, cascade.KindInvalidInput):
				refused++
			default:
				return refused, err
			}
		}
		if next == "" {
			return refused, nil
		}
		cursor = next
	}
}

// outcomeFromJob builds a TelemetryOutcome from a completed jobs.Job.
//
// HONEST GAP: jobs.Job (internal/jobs/model.go) carries no repo_id,
// language, component, lane_tier, node_id, context_size_tokens,
// retrieval_strategy, queue_time_ms, ci_failure_count, rework_cycles, or
// rollback_at signal -- no ticket has yet threaded those enrichment
// fields from a job's dispatch/execution context back onto the Job row
// itself. Each is recorded as its honest neutral default ("unknown"/0/
// false) rather than a fabricated value; a future ticket that enriches
// jobs.Job (or an execution-scoped side channel) should thread real
// values through here.
func outcomeFromJob(j jobs.Job, state jobs.JobState) TelemetryOutcome {
	return TelemetryOutcome{
		JobID:              j.ID,
		TaskClass:          labelFor(j.MinTaskClass),
		RepoID:             "unknown",
		Language:           LanguageUnknown,
		Component:          "unknown",
		RiskClass:          labelFor(j.RiskClass),
		LaneTier:           "unknown",
		NodeID:             "unknown",
		ScopeRef:           scopeRefFor(j.MutableScope),
		ContextSizeTokens:  0,
		RetrievalStrategy:  "unknown",
		DurationMS:         (j.UpdatedAt - j.CreatedAt) * 1000,
		QueueTimeMS:        0,
		RetryCount:         int(j.ConsecutiveFailedAttempts),
		CIFailureCount:     0,
		ReworkCycles:       0,
		FinalOutcome:       outcomeClassFor(state),
		RollbackAt:         nil,
		RegressionDetected: false,
	}
}
