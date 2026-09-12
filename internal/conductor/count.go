// Purpose: the sel-based model.execute leaf-dispatch door for the Count
//   verb (FIX-manifest-collision-and-conductor-seam, mirroring embed.go's
//   R-40.X10 precedent and chat.go's Chat leaf door exactly): Executor.Count
//   lets a caller that has already resolved its own provider.Selection
//   reach a pkg/provider.ModelProvider's Count verb without ever holding
//   one itself. It does not run Execute's authorize() chain, for the same
//   reason chat.go's Chat gives: provider.CountRequest carries a text and
//   an optional model name, none of the chat-shaped fields that chain
//   needs, and a caller here has already fixed its own Selection.
// Inputs: a caller-resolved provider.Selection and a provider.CountRequest.
// Outputs: a provider.CountResponse, or a taxonomy error.
// Constraints: one of exactly four leaf-door files (chat.go, embed.go,
//   count.go, stream_door.go) permitted to originate a
//   pkg/provider.ModelProvider call outside Execute/ExecuteStream's own
//   dispatch path. One terminal audit record per call.
// SPORT: conductor.count-leaf-door/ADD (FIX-manifest-collision-and-conductor-seam).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Count runs req against sel's resolved provider through the sole
// permitted Count-verb leaf-dispatch path (Pipeline.countCapability),
// gated by the same Ready() readiness check every door method uses.
func (e *Executor) Count(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error) {
	if err := e.pipeline.Ready(); err != nil {
		return provider.CountResponse{}, err
	}
	resp, err := e.pipeline.countCapability()(ctx, sel, req)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditCount(ctx, sel, req, "provider_failure", mapped)
		return provider.CountResponse{}, mapped
	}
	e.auditCount(ctx, sel, req, "success", nil)
	return resp, nil
}

// auditCount writes the one terminal audit record every Count leaf-
// dispatch call produces, mirroring auditEmbed/auditChat's shape.
func (e *Executor) auditCount(ctx context.Context, sel provider.Selection, req provider.CountRequest, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(sel.LaneID + "|" + req.Model))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.count",
		ParamsHash: paramsHash,
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
}
