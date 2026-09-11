// Purpose: the model.embed door (R-40.X10 defect fix): Executor.Embed and
//   the EmbeddingExecutorFunc adapter that let a fixed-lane caller (J/S-
//   19.T6's providers/embeddings.ProviderEmbedder) reach a
//   pkg/provider.ModelProvider's Embed verb without ever holding one
//   itself. It mirrors execute.go's Ready() gate, sole-dispatch resolution
//   through Pipeline.embedCapability, and exactly-one-audit-record
//   discipline. It does NOT run Execute's Classifier/TaskClass/Policy
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
// Constraints: this file is the only place a pkg/provider.ModelProvider
//   Embed call may originate from (R-40.X10,
//   TestSeam_NoDirectModelProviderCallOutsideConductor,
//   TestExecute_ProviderCallGraph). No second audit-redaction engine: the
//   one terminal record per call runs through the same injected
//   audit.Writer Execute already uses, so a Writer built with
//   audit.NewWithRedactor redacts this path identically.
// SPORT: conductor.embed/ADD (R-40.X10 defect fix, P1-E11-W3-S22-T1 follow-up).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Embed runs req against sel's resolved provider through the sole
// permitted Embed-verb dispatch path (Pipeline.embedCapability), gated by
// the same Ready() readiness check Execute uses: zero provider calls
// happen before all six R-21.206 collaborators are installed. A dispatch
// error is mapped onto the A-T7 taxonomy exactly like Execute's own
// mapProviderError, never surfaced raw.
func (e *Executor) Embed(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if err := e.pipeline.Ready(); err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	resp, err := e.pipeline.embedCapability()(ctx, sel, req)
	if err != nil {
		mapped := mapProviderError(err)
		e.auditEmbed(ctx, sel, req, "provider_failure", mapped)
		return provider.ModelEmbedResponse{}, mapped
	}
	e.auditEmbed(ctx, sel, req, "success", nil)
	return resp, nil
}

// auditEmbed writes the one terminal audit record every Embed call
// produces, mirroring Executor.append's shape (ExecutionTrace,
// audit.KindPolicyRoute, action "model.embed"). RiskLevel is left the zero
// value rather than a fabricated SensitivityTier: unlike Execute, Embed
// resolves no tier of its own (see this file's header).
func (e *Executor) auditEmbed(ctx context.Context, sel provider.Selection, req provider.ModelEmbedRequest, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(sel.LaneID + "|" + req.Model))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.embed",
		ParamsHash: paramsHash,
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
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
