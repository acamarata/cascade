// Purpose: the model.embed door (R-40.X10 defect fix): Executor.Embed and
//   the EmbeddingExecutorFunc adapter that let a fixed-lane caller (J/S-
//   19.T6's providers/embeddings.ProviderEmbedder) reach a
//   pkg/provider.ModelProvider's Embed verb without ever holding one
//   itself. It passes enterDoor (door.go): readiness, tier, lane admission,
//   policy and egress substitution, then one dispatch and one audit record. It does NOT run Execute's Classifier/TaskClass/Policy
//   authorize() chain: that chain is built entirely around
//   provider.ModelRequest's chat-shaped fields (TaskID, TaskClass, Inputs
//   []ChatMessage, Requirements) and Router.Select's signature is
//   provider.ModelRequest-shaped throughout; provider.ModelEmbedRequest
//   (a text batch plus an optional model name) has none of them, and the
//   frozen nine-class taskClasses table in resolve.go has no "embed" row
//   to invent one against. Forcing an embed batch through that chain would
//   mean fabricating a TaskClass and a ChatMessage-shaped Inputs the
//   embed caller never had - see the journal for this gap quoted plainly
//   rather than papered over with a fake fit.
// Inputs: a caller-resolved provider.Selection (embed callers resolve
//   their lane once at construction, like ProviderEmbedder already does;
//   see EmbeddingExecutor) and a provider.ModelEmbedRequest.
// Outputs: a provider.ModelEmbedResponse, or a taxonomy error.
// Constraints: the Embed call originates only from door.go (R-40.X10,
//   TestSeam_NoDirectModelProviderCallOutsideConductor,
//   TestDoorIsOnlyDispatchPath). No second audit-redaction engine: the
//   one terminal record per call runs through the same injected
//   audit.Writer Execute already uses, so a Writer built with
//   audit.NewWithRedactor redacts this path identically.
// SPORT: conductor.embed/ADD (R-40.X10 defect fix, P1-E11-W3-S22-T1 follow-up).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// Embed runs req against sel's lane through enterDoor and the door's Embed
// dispatch: the batch is substituted under the resolved tier before any
// provider is resolved. A dispatch error is mapped onto the A-T7 taxonomy
// exactly like Execute's own mapProviderError, never surfaced raw.
func (e *Executor) Embed(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	pass, err := e.enterDoor(ctx, doorCall{Verb: VerbEmbed, Sel: sel, Model: req.Model, Payload: req.Inputs})
	if err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	if req.Inputs, err = doorPayload[[]string](pass); err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	req.Model = pass.Model
	resp, err := e.doorEmbed(ctx, pass.Sel, req)
	if err = e.finishLeaf(ctx, VerbEmbed, pass, req.Model, err); err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	return resp, nil
}

// EmbeddingExecutorFunc adapts a fixed provider.Selection to the
// no-Selection Embed(ctx, req) shape a fixed-lane caller expects. It
// structurally satisfies providers/embeddings.EmbedExecutor without this
// package importing that one, or that package importing this one or
// pkg/provider.ModelProvider (providers -> pkg only; see this file's
// header): Go interface satisfaction needs no import on either side, only
// a matching method set. Build one with Executor.EmbeddingExecutor.
type EmbeddingExecutorFunc func(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error)

// Embed satisfies the single-method shape EmbeddingExecutorFunc adapts to.
func (f EmbeddingExecutorFunc) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return f(ctx, req)
}

// EmbeddingExecutor binds sel once, for a caller (the daemon composition
// root, wiring providers/embeddings.NewProviderEmbedder) that resolved its
// embedding lane at construction time rather than per call - the same
// granularity ProviderEmbedder already commits to via its own EmbedModel
// field.
func (e *Executor) EmbeddingExecutor(sel provider.Selection) EmbeddingExecutorFunc {
	return func(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return e.Embed(ctx, sel, req)
	}
}
