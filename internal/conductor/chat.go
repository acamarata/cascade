// Purpose: the sel-based model.execute leaf-dispatch door for the Chat verb
//   (FIX-manifest-collision-and-conductor-seam, mirroring embed.go's
//   R-40.X10 precedent exactly): Executor.Chat lets a caller that has
//   already resolved its own provider.Selection reach a
//   pkg/provider.ModelProvider's Chat verb without ever holding one
//   itself. It does NOT run Execute's Classifier/TaskClass/Policy
//   authorize() chain or Router.Select: that chain assumes a caller
//   submitting a fresh, class-routed provider.ModelRequest job for the
//   router to place; a caller here has already fixed BOTH the lane and the
//   exact model (Selection.Model) per call - e.g. providers/agents/local
//   testing one specific local model id's authoring qualification, or
//   forwarding a caller-named ChatRequest.Model verbatim. There is no
//   ModelRequest field a caller could use to pin an exact model id in the
//   first place (Requirements only carries reasoning/context/structured
//   hints for the router to match against, never a model name), so this
//   leaf door - not Execute - is the correct entry point for that need.
// Inputs: a caller-resolved provider.Selection and a provider.ChatRequest.
// Outputs: a provider.ChatResponse, or a taxonomy error.
// Constraints: this file is one of exactly four places (with embed.go,
//   count.go, stream_door.go) a pkg/provider.ModelProvider Chat/Embed/
//   Count/Stream call may originate from outside Execute/ExecuteStream's
//   own dispatch path (TestSeam_NoDirectModelProviderCallOutsideConductor,
//   TestExecute_ProviderCallGraph). One terminal audit record per call,
//   through the same injected audit.Writer Execute already uses.
// SPORT: conductor.chat-leaf-door/ADD (FIX-manifest-collision-and-conductor-seam).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Chat runs req against sel's resolved provider through the sole permitted
// Chat-verb leaf-dispatch path (Pipeline.capability, the same resolver
// Execute's own dispatchWithFailover uses), gated by the same Ready()
// readiness check every door method uses: zero provider calls happen
// before all six R-21.206 collaborators are installed.
func (e *Executor) Chat(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error) {
	if err := e.pipeline.Ready(); err != nil {
		return provider.ChatResponse{}, err
	}
	resp, err := e.pipeline.capability()(ctx, sel, req)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditChat(ctx, sel, req, "provider_failure", mapped)
		return provider.ChatResponse{}, mapped
	}
	e.auditChat(ctx, sel, req, "success", nil)
	return resp, nil
}

// auditChat writes the one terminal audit record every Chat leaf-dispatch
// call produces, mirroring auditEmbed's shape. RiskLevel is left the zero
// value: like Embed, this leaf door resolves no SensitivityTier of its own
// (the caller already resolved its own Selection before reaching here).
func (e *Executor) auditChat(ctx context.Context, sel provider.Selection, req provider.ChatRequest, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(sel.LaneID + "|" + req.Model))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.chat",
		ParamsHash: paramsHash,
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
}
