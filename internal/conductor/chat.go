// Purpose: the sel-based model.execute leaf-dispatch door for the Chat verb
//   (FIX-manifest-collision-and-conductor-seam, mirroring embed.go's
//   R-40.X10 precedent exactly): Executor.Chat lets a caller that has
//   already resolved its own provider.Selection reach a
//   pkg/provider.ModelProvider's Chat verb without ever holding one
//   itself. It does NOT run Execute's Classifier/TaskClass
//   authorize() chain or Router.Select (it passes enterDoor instead, door.go): that chain assumes a caller
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
// Constraints: every call passes enterDoor (door.go): readiness, tier (context
//   tier, restricted when absent), lane admission, policy and egress
//   substitution happen before the provider is resolved. One terminal audit
//   record per call through the same audit.Writer Execute uses.
// SPORT: conductor.chat-leaf-door/CHANGE (P1-SEC-01).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// Chat runs req against sel's lane through enterDoor and the door's Chat
// dispatch. Nothing reaches a provider before the door admits the call.
func (e *Executor) Chat(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error) {
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbChat, Sel: sel, Model: req.Model, Payload: req.Messages})
	if err != nil {
		return provider.ChatResponse{}, err
	}
	if req.Messages, err = doorPayload[[]provider.ChatMessage](pass); err != nil {
		return provider.ChatResponse{}, err
	}
	req.Model = pass.Model
	resp, err := e.doorChat(ctx, pass.Sel, req)
	if err = e.finishLeaf(ctx, VerbChat, pass, req.Model, err); err != nil {
		return provider.ChatResponse{}, err
	}
	return resp, nil
}
