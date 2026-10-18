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
// Constraints: every call passes enterDoor (door.go) before any provider is
//   resolved. One terminal audit record per call, written after sink
//   delivery completes (success or error), matching runStream's own
//   after-the-fact audit timing.
// SPORT: conductor.stream-leaf-door/CHANGE (P1-SEC-01).

package conductor

import (
	"context"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/provider"
)

// Stream runs req against sel's lane through enterDoor and the door's Stream
// dispatch; the audit record is written after the sink finishes.
func (e *Executor) Stream(ctx context.Context, sel provider.Selection, req provider.ChatRequest, sink provider.StreamSink) error {
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbStream, Sel: sel, Model: req.Model, Payload: req.Messages})
	if err != nil {
		return err
	}
	if req.Messages, err = doorPayload[[]provider.ChatMessage](pass); err != nil {
		return err
	}
	req.Model = pass.Model
	var delivered atomic.Bool
	err = e.doorStream(ctx, pass.Sel, req, &delivered, sink)
	return e.finishLeaf(ctx, VerbStream, pass, req.Model, err)
}
