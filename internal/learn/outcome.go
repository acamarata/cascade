package learn

// Purpose: TelemetryOutcome (R-21.152 allowlisted field set), OutcomeWriter,
//   SQLiteOutcomeWriter -- inserts one row, joins jobs_usage (R-16.52) for
//   cost_tokens/quota_units, propagates outcome_class via
//   internal/conductor.UpdateOutcomeClass.
// Inputs: a TelemetryOutcome (reconcile.go) and an injected runtime.Clock.
// Outputs: one jobs_telemetry_outcomes row per job_id (idempotent), one
//   jobs_usage.outcome_class update.
// Constraints: no bare time.Now (Art.7.3); UpdateOutcomeClass is the real
//   exported method, never reimplemented here.
// SPORT: internal.learn.TelemetryOutcome/ADDED, internal.learn.OutcomeWriter/ADDED,
//   internal.learn.SQLiteOutcomeWriter/ADDED (P1-E31-W6-S64-T1).

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TelemetryOutcome is R-21.152's allowlisted, structured-only record of one
// terminal job -- ONLY these fields (allowlist.go) are ever persisted.
type TelemetryOutcome struct {
	JobID              string
	TaskClass          string
	RepoID             string // opaque repo record id -- never a path/URL/org-repo name
	Language           Language
	Component          string // semantic-model component LABEL, <=64 chars
	RiskClass          string
	LaneTier           string
	NodeID             string // Q/S-36.T1 opaque node id -- never a hostname
	ScopeRef           string // the E/S-08 scope id the job ran under
	ContextSizeTokens  int64
	RetrievalStrategy  string
	DurationMS         int64
	QueueTimeMS        int64
	RetryCount         int
	CIFailureCount     int
	ReworkCycles       int
	FinalOutcome       OutcomeClass
	RollbackAt         *time.Time // nullable
	RegressionDetected bool
	CostTokens         int64
	QuotaUnits         int64
}

// OutcomeWriter is the seam reconcile.go writes through.
type OutcomeWriter interface {
	Record(ctx context.Context, o TelemetryOutcome) error
}

// SQLiteOutcomeWriter is OutcomeWriter's real modernc/sqlite impl.
type SQLiteOutcomeWriter struct {
	db    *sql.DB
	clock runtime.Clock
}

// NewSQLiteOutcomeWriter persists through db, stamping created_at via clock.
func NewSQLiteOutcomeWriter(db *sql.DB, clock runtime.Clock) *SQLiteOutcomeWriter {
	return &SQLiteOutcomeWriter{db: db, clock: clock}
}

var _ OutcomeWriter = (*SQLiteOutcomeWriter)(nil)

// Record inserts one jobs_telemetry_outcomes row for o.JobID (idempotent
// by job_id). cost_tokens/quota_units come from the per-job jobs_usage
// row (R-16.52); no row yet is zero, not a blocked write. The stored
// job_id is storedJobID(o.JobID); the usage join and the outcome_class
// update use the raw id, which is what jobs_usage is keyed by.
func (w *SQLiteOutcomeWriter) Record(ctx context.Context, o TelemetryOutcome) error {
	if w == nil || w.db == nil || w.clock == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: Record requires a constructed SQLiteOutcomeWriter")
	}
	if err := validateOutcome(o); err != nil {
		return err
	}
	rawJobID := o.JobID
	costTokens, quotaUnits, err := readUsageJoin(ctx, w.db, rawJobID)
	if err != nil {
		return err
	}
	o.CostTokens, o.QuotaUnits = costTokens, quotaUnits
	o.JobID = storedJobID(rawJobID)

	if err := w.insertOutcome(ctx, o); err != nil {
		return err
	}
	err = conductor.NewUsageStore(w.db).UpdateOutcomeClass(ctx, conductor.JobID(rawJobID), string(o.FinalOutcome))
	// ErrUsageRecordNotFound is non-fatal: Executor's usage-store has no
	// production caller yet (usage_migration.go's CONTRACT NOTE), so most
	// jobs carry no jobs_usage row today; the outcome row is already
	// committed (TestOutcomeClassPropagation covers jobs that DO have one).
	if err != nil && !errors.Is(err, conductor.ErrUsageRecordNotFound) {
		return err
	}
	return nil
}

// insertOutcome issues the INSERT, split from Record for the 50-line cap.
func (w *SQLiteOutcomeWriter) insertOutcome(ctx context.Context, o TelemetryOutcome) error {
	var rollbackAt any
	if o.RollbackAt != nil {
		rollbackAt = o.RollbackAt.UTC().Unix()
	}
	_, err := w.db.ExecContext(ctx, `
		INSERT INTO `+tableTelemetryOutcomes+`
			(job_id, task_class, repo_id, language, component, risk_class, lane_tier, node_id, scope_ref,
			 context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count,
			 ci_failure_count, rework_cycles, final_outcome, rollback_at, regression_detected,
			 cost_tokens, quota_units, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(job_id) DO NOTHING`,
		o.JobID, o.TaskClass, o.RepoID, string(o.Language), o.Component, o.RiskClass, o.LaneTier, o.NodeID, o.ScopeRef,
		o.ContextSizeTokens, o.RetrievalStrategy, o.DurationMS, o.QueueTimeMS, o.RetryCount,
		o.CIFailureCount, o.ReworkCycles, string(o.FinalOutcome), rollbackAt, boolToInt(o.RegressionDetected),
		o.CostTokens, o.QuotaUnits, w.clock.Now().UTC().Unix(),
	)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert jobs_telemetry_outcomes row")
	}
	return nil
}

// readUsageJoin reads jobs_usage for jobID (R-16.52): cost_tokens =
// tokens_in+tokens_out, quota_units = cost_micros (DECISION, T0: no
// separate "quota unit" column exists). No row yet is zero, not an error.
// Reads the table by its known name directly -- UsageStore exposes no
// read API (export.go's header documents the same shape-by-convention).
func readUsageJoin(ctx context.Context, db *sql.DB, jobID string) (costTokens, quotaUnits int64, err error) {
	var tokensIn, tokensOut, costMicros int64
	row := db.QueryRowContext(ctx,
		`SELECT tokens_in, tokens_out, cost_micros FROM jobs_usage WHERE job_id = ?`, jobID)
	switch err := row.Scan(&tokensIn, &tokensOut, &costMicros); err {
	case nil:
		return tokensIn + tokensOut, costMicros, nil
	case sql.ErrNoRows:
		return 0, 0, nil
	default:
		return 0, 0, cascade.Wrap(cascade.KindUnavailable, err, "learn: read jobs_usage row for cost/quota join")
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
