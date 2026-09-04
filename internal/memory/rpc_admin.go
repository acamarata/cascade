package memory

// Purpose: memory.consolidate — the one-shot manual trigger for the
//   consolidation job, and the [memory] config section the scheduled job
//   and this verb are both configured from.
// Inputs: raw JSON params from an untrusted peer; the raw [memory] table
//   as the config loader decoded it.
// Outputs: a ConsolidationReport, or a pkg/cascade taxonomy error.
// Constraints: params decode into a concrete struct, never interface{};
//   every refusal is a taxonomy error; no clock read outside the injected
//   Clock the Consolidator already holds; no platform-specific imports.
//   The [memory] configuration this verb and the background jobs share
//   lives in job_config.go (300-line cap split, P1-E07-W2-S14-T3).
// SPORT: internal.memory.rpc_admin.Handler (ADD, P1-E07-W2-S13-T4).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
)

// MethodConsolidate runs the consolidation job once and returns its
// report. It is a constant because the daemon registers it and the CLI
// calls it by the same name.
const MethodConsolidate = "memory.consolidate"

// ConsolidateParams is memory.consolidate's input.
type ConsolidateParams struct {
	// DryRun asks what a consolidation would merge without merging it.
	// This verb retires a user's own records with no prompt anywhere in
	// the shipped path (§5.8 forbids one), so the rehearsal is the only
	// way to look before leaping.
	DryRun bool `json:"dry_run,omitempty"`
}

// AdminHandler serves the administrative half of the memory.* namespace:
// the jobs a user or the scheduler triggers, as opposed to the per-record
// verbs Handler serves.
//
// It is a separate type from Handler rather than more methods on it
// because it needs a different collaborator — the Consolidator, which owns
// the store TREE (the record files plus the consolidation records beside
// them), where Handler needs only the MemoryStore contract.
type AdminHandler struct {
	consolidator *Consolidator
	// cfg is the [memory] configuration this daemon resolved at startup.
	// The verb does not re-read config: a manual run must consolidate by
	// the same rules the background job does, and reading configuration
	// twice is how those two drift apart.
	cfg ConsolidationConfig
}

// NewAdminHandler returns an AdminHandler over c, running with cfg. cfg's
// DryRun is ignored — the per-call parameter decides that — and only its
// EmbeddingEnabled flag carries through.
func NewAdminHandler(c *Consolidator, cfg ConsolidationConfig) *AdminHandler {
	cfg.DryRun = false
	return &AdminHandler{consolidator: c, cfg: cfg}
}

// Register binds memory.consolidate on r. This is the whole of the
// composition-root wiring: without this call the handler is built, tested
// and unreachable from a running daemon.
func (h *AdminHandler) Register(r *rpc.Registry) {
	r.Register(MethodConsolidate, h.Consolidate)
}

// Compile-time proof that the method still satisfies the router's handler
// signature, so a drifting signature fails the build here rather than at
// the composition root.
var _ rpc.HandlerFunc = (*AdminHandler)(nil).Consolidate

// Consolidate serves memory.consolidate.
//
// # What it does
//
// It runs exactly one consolidation pass (see
// Consolidator.ConsolidateMemories for the algorithm and the crash
// contract) and returns its report. With DryRun it runs the whole grouping
// pass and reports what it WOULD retire, writing nothing and emitting
// nothing.
//
// # The advisory lock
//
// Exclusion between daemons is the scheduler's advisory lock (C/S-04.T4):
// only the lock holder ticks, so only one daemon ever runs the scheduled
// job. Within one process, this verb and the scheduled job share a
// Consolidator and its re-entrancy guard — a call that arrives while a run
// is in flight returns ConsolidationReport{Skipped:true} and a nil error
// rather than queueing behind a background job or racing it.
//
// # Errors
//
// KindInvalidInput for malformed params; KindUnsupported when
// [memory].consolidation_embedding is on, which this build cannot serve;
// KindIntegrity for a damaged consolidation record; KindUnavailable when
// the file system fails.
func (h *AdminHandler) Consolidate(ctx context.Context, params json.RawMessage) (any, error) {
	var p ConsolidateParams
	if err := decodeParams(MethodConsolidate, params, &p); err != nil {
		return nil, err
	}
	cfg := h.cfg
	cfg.DryRun = p.DryRun
	report, err := h.consolidator.ConsolidateMemories(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return report, nil
}
