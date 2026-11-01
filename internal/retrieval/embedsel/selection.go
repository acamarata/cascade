// Package embedsel holds the one embedder Selection a process composes for
// retrieval: which embedder runs, where it came from, and which sensitivity
// tiers it may be handed.
//
// Purpose: one value the recall vector leg, the recall.what files leg,
// recall.index.* and the retrieval_index doctor check all read, so they
// cannot disagree about which embedding space an index is in.
//
// Inputs: an embedder built by the composition root (the local BGE-M3
// sidecar client, or a conductor-lane embedder), or the reason none is.
//
// Outputs: a *Selection.
//
// Constraints: this package imports pkg/provider, internal/retrieval/fusion
// and internal/retrieval/corpus only (and the pkg/cascade taxonomy); it
// never builds an embedder itself. A Selection that is not usable (not
// configured, or configured and refused) admits no tier and every Embed
// returns its error: there is no fallback to another embedder.
//
// SPORT: internal.retrieval.embedsel.Selection/ADDED (P1-CPA-11).
package embedsel

import (
	"context"
	"slices"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The three Source values.
const (
	// SourceLocalSidecar is the local BGE-M3 sidecar over a unix socket.
	SourceLocalSidecar = "local-sidecar"
	// SourceConductorLane is a conductor lane, reached through the
	// in-process conductor door.
	SourceConductorLane = "conductor-lane"
	// SourceNotConfigured is the selection when neither is configured.
	SourceNotConfigured = "not-configured"
)

// Selection is the process's embedder choice.
type Selection struct {
	embedder provider.Embedder
	source   string
	model    string
	dims     int
	admits   []provider.SensitivityTier
	err      error
}

// New returns a usable Selection over e, admitting exactly the tiers in
// admits (an invalid tier in the list is ignored). A nil e is refused: the
// result is Failed(source, KindInvalidInput).
func New(e provider.Embedder, source, model string, dims int, admits []provider.SensitivityTier) *Selection {
	if e == nil {
		return Failed(source, cascade.Newf(cascade.KindInvalidInput, "embedsel: no embedder for source %q", source))
	}
	set := make([]provider.SensitivityTier, 0, len(admits))
	for _, t := range admits {
		if t.Valid() && !slices.Contains(set, t) {
			set = append(set, t)
		}
	}
	return &Selection{embedder: e, source: source, model: model, dims: dims, admits: set}
}

// NotConfigured returns the selection of a process with no embedder
// configured. err says what is unset; a nil err is replaced by a generic
// KindUnavailable.
func NotConfigured(err error) *Selection {
	return Failed(SourceNotConfigured, err)
}

// Failed returns the selection of a configured source that was refused or
// could not be built. It admits no tier and every Embed returns err; the
// caller never routes its content to another source instead.
func Failed(source string, err error) *Selection {
	if err == nil {
		err = cascade.Newf(cascade.KindUnavailable, "embedsel: embedder %q is not available", source)
	}
	return &Selection{embedder: refusingEmbedder{err: err}, source: source, admits: nil, err: err}
}

// Embedder returns the selected embedder. It is never nil: an unusable
// selection's embedder refuses every call with Err.
func (s *Selection) Embedder() provider.Embedder { return s.embedder }

// Model returns the embedding model id ("" when unusable).
func (s *Selection) Model() string { return s.model }

// Dimensions returns the embedding width (0 when unusable).
func (s *Selection) Dimensions() int { return s.dims }

// Source returns SourceLocalSidecar, SourceConductorLane or
// SourceNotConfigured.
func (s *Selection) Source() string { return s.source }

// Err returns why the selection is unusable, nil when it is usable.
func (s *Selection) Err() error { return s.err }

// Configured reports whether a source was configured, usable or not.
func (s *Selection) Configured() bool { return s.source != SourceNotConfigured }

// Admits reports whether content at tier t may be handed to the embedder.
func (s *Selection) Admits(t provider.SensitivityTier) bool { return slices.Contains(s.admits, t) }

// AdmitsCorpus reports whether c's content may be handed to the embedder.
func (s *Selection) AdmitsCorpus(c corpus.Corpus) bool { return s.Admits(PrivacyTier(c.Privacy)) }

// FusionEmbedder adapts the embedder to the vector leg's narrow interface.
func (s *Selection) FusionEmbedder() fusion.Embedder { return fusionEmbedder{inner: s.embedder} }

// PrivacyTier maps a corpus privacy class onto the tier its content is
// embedded at: personal is local-only, global and project are internal,
// and an invalid class is local-only. It is fusion.PrivacyTier, the one
// mapping the vector leg also stamps queries with.
func PrivacyTier(c corpus.PrivacyClass) provider.SensitivityTier { return fusion.PrivacyTier(c) }

// refusingEmbedder is an unusable selection's embedder: every call is err.
type refusingEmbedder struct{ err error }

// Model reports no embedding space.
func (r refusingEmbedder) Model() provider.EmbedModel { return provider.EmbedModel{} }

// Embed refuses.
func (r refusingEmbedder) Embed(context.Context, []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	return nil, r.err
}

// fusionEmbedder turns texts into EmbedInputs and outputs into vectors.
type fusionEmbedder struct{ inner provider.Embedder }

// Embed embeds texts, refusing a reply that does not answer every input.
func (f fusionEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	inputs := make([]provider.EmbedInput, len(texts))
	for i, t := range texts {
		inputs[i] = provider.EmbedInput{Text: t}
	}
	outputs, err := f.inner.Embed(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if len(outputs) != len(texts) {
		return nil, cascade.Newf(cascade.KindIntegrity, "embedsel: embedder returned %d vectors for %d texts", len(outputs), len(texts))
	}
	out := make([][]float32, len(outputs))
	for i, o := range outputs {
		out[i] = o.Vector
	}
	return out, nil
}
