// Package embeddings holds the api-backed Embedder (R-14.32): a concrete
// provider.Embedder implementation adapting any pkg/provider.ModelProvider
// driver's Embed verb to the retrieval-side Embedder interface.
//
// Purpose: the api-backed Embedder (R-14.32): a concrete provider.Embedder
//
//	that routes every Embed call through an injected provider.ModelProvider's
//	Embed verb (J/S-19.T1), rather than speaking to any embedding backend
//	directly. It is the thin adapter F-S10.T3's embed pipeline composes
//	with once a ModelProvider driver (T2 anthropic, T3 openai-compat, T4
//	gemini, T5 ollama) is wired.
//
// Inputs: a ModelProvider, the embedding space it is configured to
//
//	produce, the caller-declared SensitivityTier for the content this
//	instance embeds, and an Interceptor seam every request transits before
//	any text leaves this process.
//
// Outputs: one provider.EmbedOutput per provider.EmbedInput, in order, or a
//
//	pkg/cascade taxonomy error. Never a zero or partial vector (Art.1.1).
//
// Constraints: imports pkg/provider and pkg/cascade only (02-TARGET-
//
//	STRUCTURE.md §v1.1: providers -> pkg only); no dedup, no batching
//	policy, no wire decoding here - those stay F-S10.T3's and the
//	ModelProvider driver's, respectively (06-FORGE-SPEC.md §5 rule 1).
//
// SPORT: providers.embeddings.ProviderEmbedder/ADD (P1-E10-W3-S19-T6).
package embeddings

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EgressClass names the outbound destination class an embed request
// transits under (R-21.265). It mirrors the shape of
// internal/hooks/egress.EgressClass without importing that package: the
// providers -> pkg import boundary forbids reaching into internal/, so
// this package declares the minimal seam it needs and a composition root
// elsewhere adapts the real egress engine to satisfy Interceptor.
type EgressClass string

// EgressClassConductor is the class every ProviderEmbedder request
// transits (R-21.265): an embedding request is conductor-mediated
// provider traffic like any other model.execute call, not a side door.
// The struck depguard/arch allowlist entry this class replaces is
// recorded in this ticket's journal.
const EgressClassConductor EgressClass = "conductor"

// Interceptor is the sensitivity-egress choke point (K/S-22.T3's Intercept
// under EgressClassConductor) every ProviderEmbedder request transits
// before any input text reaches a ModelProvider. It is declared locally
// rather than imported from internal/hooks/egress (providers -> pkg only;
// see EgressClass's doc), so the concrete Engine-backed implementation is
// wired in by whatever composes this package with the rest of the daemon.
//
// InterceptClass returns bytes that are safe to send on class, or an
// error and nothing to send. A ProviderEmbedder built with an Interceptor
// that always errors is a ProviderEmbedder that always fails closed,
// which is the correct behaviour for an adapter nobody has wired a real
// engine into yet.
type Interceptor interface {
	InterceptClass(ctx context.Context, class EgressClass, tier provider.SensitivityTier, content []byte) ([]byte, error)
}

// ProviderEmbedder is the api-backed provider.Embedder implementation
// (R-14.32): it holds a ModelProvider and routes every Embed call through
// that provider's own Embed verb, after transiting every input through
// Interceptor under EgressClassConductor with this instance's Sensitivity
// tier, unchanged (R-21.265).
//
// ProviderEmbedder never widens the tier it was constructed with, never
// invents a vector when the underlying ModelProvider or Interceptor
// refuses, and performs no content-hash dedup: dedup is F-S10.T3's
// pipeline-layer responsibility, not this adapter's (06 §5 rule 1).
type ProviderEmbedder struct {
	mp        provider.ModelProvider
	model     provider.EmbedModel
	tier      provider.SensitivityTier
	intercept Interceptor
}

// ProviderEmbedder satisfies provider.Embedder. The assertion lives here
// so a change to either side breaks the build rather than the seam.
var _ provider.Embedder = (*ProviderEmbedder)(nil)

// NewProviderEmbedder builds a ProviderEmbedder over mp, producing vectors
// in the embedding space model names, for content classified at tier.
//
// Every dependency is required and validated up front: a nil mp or
// intercept, or an unset model identity (empty ID or non-positive
// Dimensions), is refused rather than accepted and left to fail
// confusingly on the first Embed call (Art.1). An invalid tier (outside
// the four declared SensitivityTier members) resolves to
// provider.SensitivityRestricted rather than being rejected, matching the
// fail-closed rule that an unresolvable tier never reads as permissive
// (06 §5 rule 15).
func NewProviderEmbedder(mp provider.ModelProvider, model provider.EmbedModel, tier provider.SensitivityTier, intercept Interceptor) (*ProviderEmbedder, error) {
	if mp == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "embeddings: NewProviderEmbedder requires a non-nil ModelProvider")
	}
	if model.ID == "" || model.Dimensions <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"embeddings: NewProviderEmbedder requires a model naming an id and a positive width, got %q/%d",
			model.ID, model.Dimensions)
	}
	if intercept == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "embeddings: NewProviderEmbedder requires a non-nil Interceptor")
	}
	if !tier.Valid() {
		tier = provider.SensitivityRestricted
	}
	return &ProviderEmbedder{mp: mp, model: model, tier: tier, intercept: intercept}, nil
}

// Model returns the embedding space this ProviderEmbedder is configured
// for. Constant for its lifetime; performs no I/O.
func (p *ProviderEmbedder) Model() provider.EmbedModel {
	return p.model
}

// Embed satisfies provider.Embedder.Embed: it transits every input through
// Interceptor under EgressClassConductor with this instance's Sensitivity
// tier unchanged, batches the (possibly rewritten) texts into one
// ModelProvider.Embed call, and returns one EmbedOutput per input in
// order.
//
// A refusal at either seam - the Interceptor or the underlying
// ModelProvider - is returned unchanged: this is what lets a provider
// whose Embed verb has no backend (anthropic's KindUnsupported) refuse a
// caller honestly instead of this adapter converting that refusal into an
// empty or zero vector (Art.1.1).
func (p *ProviderEmbedder) Embed(ctx context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	if len(inputs) == 0 {
		return []provider.EmbedOutput{}, nil
	}
	texts, err := p.interceptInputs(ctx, inputs)
	if err != nil {
		return nil, err
	}
	resp, err := p.mp.Embed(ctx, provider.ModelEmbedRequest{Inputs: texts, Model: p.model.ID})
	if err != nil {
		return nil, err
	}
	return p.decodeResponse(inputs, resp)
}

// interceptInputs runs every input's text through Interceptor under
// EgressClassConductor, in order, returning the safe-to-send texts. It
// stops and fails closed at the first seam error rather than intercepting
// the remainder: a caller that receives an error must never receive a
// partially-checked batch.
func (p *ProviderEmbedder) interceptInputs(ctx context.Context, inputs []provider.EmbedInput) ([]string, error) {
	texts := make([]string, len(inputs))
	for i, in := range inputs {
		safe, err := p.intercept.InterceptClass(ctx, EgressClassConductor, p.tier, []byte(in.Text))
		if err != nil {
			return nil, err
		}
		texts[i] = string(safe)
	}
	return texts, nil
}

// decodeResponse validates resp against inputs and builds the positionally
// corresponding []provider.EmbedOutput. A vector count that does not match
// len(inputs), or any vector not exactly p.model.Dimensions wide, is a
// typed KindIntegrity error rather than a truncated or padded result.
func (p *ProviderEmbedder) decodeResponse(inputs []provider.EmbedInput, resp provider.ModelEmbedResponse) ([]provider.EmbedOutput, error) {
	if len(resp.Vectors) != len(inputs) {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"embeddings: provider returned %d vectors for %d inputs", len(resp.Vectors), len(inputs))
	}
	outputs := make([]provider.EmbedOutput, len(inputs))
	for i, vec := range resp.Vectors {
		if len(vec) != p.model.Dimensions {
			return nil, cascade.Newf(cascade.KindIntegrity,
				"embeddings: provider returned a %d-dimension vector at index %d, want %d",
				len(vec), i, p.model.Dimensions)
		}
		outputs[i] = provider.EmbedOutput{Vector: vec, Model: p.model}
	}
	return outputs, nil
}
