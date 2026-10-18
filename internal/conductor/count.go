// Purpose: the sel-based model.execute leaf-dispatch door for the Count
//   verb (FIX-manifest-collision-and-conductor-seam, mirroring embed.go's
//   R-40.X10 precedent and chat.go's Chat leaf door exactly): Executor.Count
//   lets a caller that has already resolved its own provider.Selection
//   reach a pkg/provider.ModelProvider's Count verb without ever holding
//   one itself. It passes enterDoor (door.go), not Execute's classifier chain: a
//   CountRequest has none of the chat-shaped fields that chain needs.
// Inputs: a caller-resolved provider.Selection and a provider.CountRequest.
// Outputs: a provider.CountResponse, or a taxonomy error.
// Constraints: every call passes enterDoor (door.go) before any provider is
//   resolved. One terminal audit record per call.
// SPORT: conductor.count-leaf-door/CHANGE (P1-SEC-01).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// Count runs req against sel's lane through enterDoor and the door's Count
// dispatch.
func (e *Executor) Count(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error) {
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbCount, Sel: sel, Model: req.Model, Payload: req.Text})
	if err != nil {
		return provider.CountResponse{}, err
	}
	if req.Text, err = doorPayload[string](pass); err != nil {
		return provider.CountResponse{}, err
	}
	req.Model = pass.Model
	resp, err := e.doorCount(ctx, pass.Sel, req)
	if err = e.finishLeaf(ctx, VerbCount, pass, req.Model, err); err != nil {
		return provider.CountResponse{}, err
	}
	return resp, nil
}
