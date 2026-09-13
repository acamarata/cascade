// Purpose: R-16.52 usage and cost attribution. UsageRecord is the per-job
//   row this ticket writes into the jobs-domain `jobs_usage` table
//   (usage_migration.go owns the schema and the store); attributeUsage is
//   the terminal-outcome hook Execute (execute.go) calls on its two
//   post-dispatch branches (success, provider error/cancellation) - never
//   on the fan-out parent branch (R-21.214), which never calls Execute a
//   second time for itself (fanout.go's assembleFanOutParent only sums
//   Legs' own Usage). The same hook also calls the J/S-20.T4 aggregate
//   (internal/providers/usage.Manager.IncrementUsage) with the identical
//   outcome data. The two writes are independent and fail open: an error
//   from either is logged at warn and published as a "usage_record_failed"
//   event, never returned to Execute's caller (full_desc point 2).
// Inputs: the ModelRequest/Selection/attempt/Usage/error a terminal Execute
//   outcome already computed, plus ctx (for requesting-entity resolution).
// Outputs: at most one UsageRecord row and one IncrementUsage call per
//   terminal outcome; zero of either for a request that never reached
//   dispatch (no lane, egress substitution failure - see this file's
//   CONTRACT NOTE below) or for the fan-out parent.
// Constraints: no bare time.Now (wall_ms comes from the pipeline's injected
//   Clock); UsageStore/UsageAggregator/CostEstimator are collaborators
//   injected via Set* methods on *Executor, exactly like SetEventBridge/
//   SetSpawnHook (execute.go's own header comment: ExecutorConfig/
//   NewExecutor are outside this ticket's files_scope) - a nil collaborator
//   makes its half of the hook a documented no-op, the same nil-safety
//   those two already use.
// SPORT: conductor.usage-attribution/ADD (P1-E11-W3-S23-T4).
//
// CONTRACT NOTE (both sides quoted, R-16.79): full_desc's numbered
// attribution-hook step lists the terminal points as "completed / cancelled
// via job.cancel / provider error" - all three are POST-dispatch outcomes
// of dispatchWithFailover. Execute's two PRE-dispatch refusals (no lane at
// line ~101, egress substitution failure at line ~106) never reach that
// enumeration: no lane_id was ever resolved and no tokens were ever spent,
// so this ticket does not attribute usage for them. "Cancelled via
// job.cancel" is exercised through Execute's OWN cancel registration
// (execute.go's e.cancels().register(jobID, cancel) at line 93, the same
// registry job.cancel (cancel.go) and streaming both use): a job.cancel
// call mid-dispatch cancels dctx, dispatchWithFailover's dispatch() call
// returns ctx's error, and that lands in the ordinary provider-error branch
// with whatever partial Usage the driver returned - see
// TestUsage_CancelledDispatch.

package conductor

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/pkg/provider"
)

// outcomeUnknown is UsageRecord.OutcomeClass's default value (R-16.33).
// This ticket always writes outcomeUnknown; AE/S-64.T1's later
// UpdateOutcomeClass call is the only path that ever sets "accepted" or
// "rejected" - unexported (and those two values not declared here at all)
// since nothing in this ticket's shipping code writes either: AE/S-64.T1
// is expected to declare its own exported vocabulary constants here when
// it lands, rather than this ticket shipping two constants no production
// path yet uses (R-16.79's "wire it, don't allow-list it").
const outcomeUnknown = "unknown"

// UsageRecord is one terminal dispatch's usage/cost row, persisted 1:1 into
// the jobs-domain jobs_usage table (PK JobID) by *UsageStore (see
// usage_migration.go). Field set and meaning per R-16.52/R-16.33.
type UsageRecord struct {
	JobID            JobID
	LaneID           string
	TaskClass        string
	TokensIn         int64
	TokensOut        int64
	CostMicroUSD     int64
	WallMS           int64
	OutcomeClass     string
	Attempt          int
	RequestingEntity string
}

// UsageRecorder is the injected per-job write seam attributeUsage calls.
// *UsageStore (usage_migration.go) satisfies it in production; tests
// substitute a recording/erroring double.
type UsageRecorder interface {
	WriteUsageRecord(ctx context.Context, r UsageRecord) error
}

// UsageAggregator is the injected J/S-20.T4 seam attributeUsage calls with
// the SAME terminal-outcome data WriteUsageRecord received. Declared
// locally (matching Router/ProviderResolver's own pattern in model.go)
// rather than requiring callers to hold a concrete *usage.Manager, so a
// test can substitute a recording/erroring double. This is NOT the
// AccountingStore/RecordUsage seam R-16.52 closes - it names exactly one
// method, matching usage.Manager.IncrementUsage's real signature, and K
// and J remain two independent stores with no shared interface.
type UsageAggregator interface {
	IncrementUsage(ctx context.Context, req usage.IncrementRequest) error
}

// CostEstimator computes a dispatch's cost_micros from its provider/model
// and token counts (the S-20.T2 rate card, per full_desc task 4). Declared
// as a seam - not called with a real registry-backed implementation by
// this ticket, see SetCostEstimator's doc comment - so a nil estimator
// (the default) yields CostMicroUSD 0 rather than blocking attribution on
// a collaborator this ticket's files_scope has no path to construct.
type CostEstimator interface {
	EstimateCostMicroUSD(providerName, model string, tokensIn, tokensOut int64) int64
}

// pluginIDContextKey / sessionIDContextKey are the unexported context keys
// resolveRequestingEntity reads. No exported setter is added by this
// ticket: neither producer exists yet (the plugin host, O/S-31.*, and the
// session layer, L/S-24.*), and an exported setter with zero callers
// anywhere would itself be a test-only-gate violation this ticket must not
// introduce (R-16.79's "wire it, don't allow-list it"). The consuming
// ticket adds an exported With*/context setter here, against these same
// key types, when it lands.
type pluginIDContextKey struct{}
type sessionIDContextKey struct{}

// resolveRequestingEntity implements the priority order full_desc's
// "Requesting entity resolution" section specifies: plugin_id beats
// session_id beats "standalone".
func resolveRequestingEntity(ctx context.Context) string {
	if v, ok := ctx.Value(pluginIDContextKey{}).(string); ok && v != "" {
		return "plugin:" + v
	}
	if v, ok := ctx.Value(sessionIDContextKey{}).(string); ok && v != "" {
		return "session:" + v
	}
	return "standalone"
}

// SetUsageStore injects s as e's per-job jobs-domain write target. A nil
// store (the default) makes WriteUsageRecord a no-op half of the hook -
// mirrors SetEventBridge's nil-safety exactly. Returns e so composition-
// root code can chain it onto NewExecutor's result.
func (e *Executor) SetUsageStore(s UsageRecorder) *Executor {
	e.usageStore = s
	return e
}

// SetUsageAggregator injects a as e's J/S-20.T4 aggregate write target. A
// nil aggregator (the default) makes IncrementUsage a no-op half of the
// hook.
func (e *Executor) SetUsageAggregator(a UsageAggregator) *Executor {
	e.usageAgg = a
	return e
}

// SetCostEstimator injects c as e's S-20.T2 rate-card seam. A nil
// estimator (the default) makes every UsageRecord's CostMicroUSD 0 rather
// than blocking attribution: this ticket's files_scope (internal/
// conductor only) has no path to the provider registry's CostRecord, so
// no production caller wires a real estimator here yet - the daemon
// composition root that constructs the Executor (tracked by
// internal/build/testonly-allow.json against P1-E11-W3-S22-T4, the same
// ticket SetSpawnHook's own production wiring is tracked against) is
// expected to call this alongside SetUsageStore/SetUsageAggregator once it
// exists.
func (e *Executor) SetCostEstimator(c CostEstimator) *Executor {
	e.costEstimator = c
	return e
}

// UsageEventNamespace is the internal/events.Bus namespace
// attributeUsage's failure notice publishes to - distinct from
// conductorEventNamespace (stream.go's per-job SSE namespace) since a
// usage-write failure is an operational signal, not part of a job's own
// SSE stream.
const UsageEventNamespace = "conductor.usage"

// EventKindUsageRecordFailed is published when either accounting write in
// attributeUsage returns an error (full_desc task 4's "usage_record_failed"
// event).
const EventKindUsageRecordFailed events.EventKind = "usage_record_failed"

// usageFailurePayload is EventKindUsageRecordFailed's JSON payload. No
// credential or model content is ever placed here.
type usageFailurePayload struct {
	JobID string `json:"job_id"`
	Write string `json:"write"`
	Error string `json:"error"`
}

// usageClockStart returns the pipeline's injected Clock's current instant
// (never bare time.Now, Art.7.3), or the zero time.Time if no pipeline or
// Clock is installed - attributeUsage's own nil/IsZero checks treat that
// as "unknown", never a bogus wall_ms.
func (e *Executor) usageClockStart() time.Time {
	if e.pipeline != nil && e.pipeline.cfg.Clock != nil {
		return e.pipeline.cfg.Clock.Now()
	}
	return time.Time{}
}

// attributeUsage builds a UsageRecord from one terminal Execute outcome
// and writes it via both R-16.52 stores. Called from Execute's two
// post-dispatch branches only (execute.go) - never from the fan-out
// parent branch; see this file's header comment. dispatchErr is the
// outcome's own error (nil on success), used only for
// IncrementRequest.Error - it is never returned or wrapped here.
func (e *Executor) attributeUsage(
	ctx context.Context,
	req provider.ModelRequest,
	jobID JobID,
	sel provider.Selection,
	attempt int,
	u provider.Usage,
	dispatchErr error,
	start time.Time,
) {
	var wallMS int64
	if e.pipeline != nil && e.pipeline.cfg.Clock != nil && !start.IsZero() {
		wallMS = e.pipeline.cfg.Clock.Now().Sub(start).Milliseconds()
	}
	rec := UsageRecord{
		JobID:            jobID,
		LaneID:           sel.LaneID,
		TaskClass:        req.TaskClass,
		TokensIn:         int64(u.InputTokens),
		TokensOut:        int64(u.OutputTokens),
		WallMS:           wallMS,
		OutcomeClass:     outcomeUnknown,
		Attempt:          attempt,
		RequestingEntity: resolveRequestingEntity(ctx),
	}
	if e.costEstimator != nil {
		rec.CostMicroUSD = e.costEstimator.EstimateCostMicroUSD(sel.Provider, sel.Model, rec.TokensIn, rec.TokensOut)
	}

	// A cancellation-independent context: neither write should be
	// skipped just because the job's own ctx is already Done (mirrors
	// publishTerminal's context.WithoutCancel use in stream.go).
	dctx := context.WithoutCancel(ctx)

	if e.usageStore != nil {
		if err := e.usageStore.WriteUsageRecord(dctx, rec); err != nil {
			e.warnUsageFailure(dctx, jobID, "write_usage_record", err)
		}
	}
	if e.usageAgg != nil {
		incErr := e.usageAgg.IncrementUsage(dctx, usage.IncrementRequest{
			ProviderName: sel.Provider,
			LaneName:     sel.LaneID,
			ModelName:    sel.Model,
			TokensIn:     rec.TokensIn,
			TokensOut:    rec.TokensOut,
			Error:        dispatchErr != nil,
			CostMicroUSD: rec.CostMicroUSD,
		})
		if incErr != nil {
			e.warnUsageFailure(dctx, jobID, "increment_usage", incErr)
		}
	}
}

// warnUsageFailure logs which accounting write failed at warn level and
// publishes EventKindUsageRecordFailed through e.bridge, if one is
// installed (nil bridge: silent no-op for the publish half, matching
// publishTerminal/publishDelta's own nil-check in stream.go). Never
// returns an error - the whole point of this hook is that neither write's
// failure ever reaches Execute's caller.
func (e *Executor) warnUsageFailure(ctx context.Context, jobID JobID, which string, err error) {
	slog.Default().Warn("conductor: usage attribution write failed",
		"job_id", string(jobID), "write", which, "error", err.Error())
	if e.bridge == nil {
		return
	}
	payload, merr := json.Marshal(usageFailurePayload{JobID: string(jobID), Write: which, Error: err.Error()})
	if merr != nil {
		return
	}
	_, _ = e.bridge.Publish(ctx, UsageEventNamespace, EventKindUsageRecordFailed, conductorEventSource, payload)
}
