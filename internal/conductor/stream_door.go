// Purpose: the sel-based model.execute leaf-dispatch door for the Stream
//   verb (FIX-manifest-collision-and-conductor-seam, mirroring embed.go's
//   R-40.X10 precedent and chat.go/count.go's leaf doors exactly):
//   Executor.Stream lets a caller that has already resolved its own
//   provider.Selection reach a pkg/provider.ModelProvider's Stream verb
//   without ever holding one itself. Unlike ExecuteStream (the router-
//   driven, channel-based, cancellable form execute.go/stream.go own),
//   this is the plain (ctx, req, sink) error leaf form ModelProvider.Stream
//   itself declares - no channel bridging, no CancelFunc, so a caller
//   outside this package can hold the matching interface with no adapter
//   type needed (unlike Embed's EmbeddingExecutorFunc, needed only because
//   its caller wanted a no-Selection convenience shape).
// Inputs: a caller-resolved provider.Selection, a provider.ChatRequest, and
//   a provider.StreamSink.
// Outputs: nil, or a taxonomy error; StreamEvent deliveries happen through
//   sink exactly as ModelProvider.Stream's own contract specifies.
// Constraints: one of exactly four leaf-door files (chat.go, embed.go,
//   count.go, stream_door.go) permitted to originate a
//   pkg/provider.ModelProvider call outside Execute/ExecuteStream's own
//   dispatch path. One terminal audit record per call, written after sink
//   delivery completes (success or error), matching runStream's own
//   after-the-fact audit timing.
// SPORT: conductor.stream-leaf-door/ADD (FIX-manifest-collision-and-conductor-seam).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Stream runs req against sel's resolved provider through the sole
// permitted Stream-verb leaf-dispatch path (Pipeline.streamCapability),
// gated by the same Ready() readiness check every door method uses.
func (e *Executor) Stream(ctx context.Context, sel provider.Selection, req provider.ChatRequest, sink provider.StreamSink) error {
	if err := e.pipeline.Ready(); err != nil {
		return err
	}
	err := e.pipeline.streamCapability()(ctx, sel, req, sink)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditStream(ctx, sel, req, "provider_failure", mapped)
		return mapped
	}
	e.auditStream(ctx, sel, req, "success", nil)
	return nil
}

// auditStream writes the one terminal audit record every Stream leaf-
// dispatch call produces, mirroring auditEmbed/auditChat/auditCount's
// shape.
func (e *Executor) auditStream(ctx context.Context, sel provider.Selection, req provider.ChatRequest, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(sel.LaneID + "|" + req.Model))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.stream",
		ParamsHash: paramsHash,
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
}
