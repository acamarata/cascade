// Purpose: ModelExecutor, the sel-based model.execute leaf-dispatch seam
//   driver.go/qualification.go hold instead of a raw provider.ModelProvider
//   (FIX-manifest-collision-and-conductor-seam, split out of driver.go
//   purely to stay under Art.10.3's 300-line/file cap once this fix's doc
//   comment landed there — same pattern audit_helpers.go's own split-out
//   note documents).
//
// CONTRACT CORRECTION (this fix, not S-62.T1's own scope): driver.go and
// qualification.go used to hold a raw provider.ModelProvider and call its
// Chat/Embed/Count/Stream methods directly. internal/conductor is the sole
// permitted caller of a ModelProvider verb
// (TestSeam_NoDirectModelProviderCallOutsideConductor,
// TestExecute_ProviderCallGraph): every other caller must reach it through
// a conductor-mediated door, never hold one itself, so sensitivity/
// routing/quota enforcement cannot be silently bypassed by a lane that
// skips straight past them. Execute's own router-driven model.execute
// door does not fit here: provider.ModelRequest has no field a caller can
// use to pin an exact model id (Requirements only carries reasoning/
// context/structured hints for the router to match against), while this
// package's entire authoring-qualification design is PER EXACT MODEL ID
// (ChatRequest.Model, Qualifier's per-model config rows) — forcing these
// calls through Execute would mean the router, not the caller, decides
// which model actually runs, breaking the very guarantee Qualifier exists
// to enforce. Instead every call now routes through the sel-based LEAF-
// dispatch doors this fix added to internal/conductor (Executor.Chat/
// Embed/Count/Stream in chat.go/embed.go/count.go/stream_door.go),
// mirroring the Embed verb's own pre-existing R-40.X10 precedent: a
// caller-resolved provider.Selection reaches the real provider without
// re-running Execute's Classifier/TaskClass/Policy chain. ModelExecutor
// below is declared locally, structurally satisfied by *conductor.Executor
// with no import in either direction (providers -> pkg only, Art.7.2),
// exactly like providers/embeddings.EmbedExecutor's own precedent.
// Inputs: n/a (interface + pure helper only).
// Outputs: the ModelExecutor interface; modelSelection's provider.Selection.
// Constraints: imports pkg/provider only (Art.7.2).
// SPORT: providers.agents.local/CHANGE (model-seam: raw ModelProvider ->
//   ModelExecutor) — FIX-manifest-collision-and-conductor-seam.

package local

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// ModelExecutor is the sel-based model.execute leaf-dispatch door every
// Driver/Qualifier call transits: this package's replacement for holding a
// raw provider.ModelProvider directly (see this file's package doc
// comment). Each verb takes a caller-resolved provider.Selection per call,
// rather than binding one at construction, because this package resolves a
// DIFFERENT model id per call (ChatRequest.Model/CountRequest.Model/
// ModelEmbedRequest.Model, Qualifier.runCases's modelID parameter) — a
// construction-time-bound executor (like providers/embeddings'
// EmbedExecutor) would only ever fit a single fixed model. Capabilities is
// deliberately excluded: Driver.Capabilities never calls this seam at all
// (it returns a fixed local posture — see driver.go's Capabilities doc).
// internal/conductor.Executor's Chat/Embed/Count/Stream leaf-door methods
// satisfy this interface structurally, with no import on either side.
type ModelExecutor interface {
	// Chat is the Chat-verb leaf-dispatch call (mirrors
	// internal/conductor.Executor.Chat's exact signature).
	Chat(ctx context.Context, sel provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error)
	// Embed is the Embed-verb leaf-dispatch call (mirrors
	// internal/conductor.Executor.Embed's exact, pre-existing R-40.X10
	// signature).
	Embed(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error)
	// Count is the Count-verb leaf-dispatch call (mirrors
	// internal/conductor.Executor.Count's exact signature).
	Count(ctx context.Context, sel provider.Selection, req provider.CountRequest) (provider.CountResponse, error)
	// Stream is the Stream-verb leaf-dispatch call (mirrors
	// internal/conductor.Executor.Stream's exact signature).
	Stream(ctx context.Context, sel provider.Selection, req provider.ChatRequest, sink provider.StreamSink) error
}

// modelSelection builds the per-call provider.Selection this package
// passes to every ModelExecutor verb: Model names the exact model id the
// caller asked for (empty defers to the resolver's own default, exactly
// as ChatRequest.Model's own doc already promises); LaneID/Provider are
// left zero, since only the injected ModelExecutor's own resolver — never
// this package (providers -> pkg only forbids it from knowing lane
// topology) — can name them meaningfully.
func modelSelection(modelID string) provider.Selection {
	return provider.Selection{Model: modelID}
}
