// Purpose: shared test doubles for the ProviderEmbedder unit lane
//
//	(apibacked_test.go): a fake EmbedExecutor, a fake Interceptor that
//	records what it receives, and the fixtures/builders every test in
//	that file composes with. Split into its own file so apibacked_test.go
//	stays under the 300-line file cap.
//
// Inputs: none external.
// Outputs: none - this file is test support only.
// Constraints: imports neither "net" nor "net/http" (Art.7.2's default
//
//	unit lane).
//
// SPORT: providers.embeddings.ProviderEmbedder/ADD (P1-E10-W3-S19-T6).
package embeddings

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeEmbedExecutor is a minimal EmbedExecutor test double: the R-40.X10
// fix's replacement for a raw provider.ModelProvider, holding only the one
// verb ProviderEmbedder ever calls.
type fakeEmbedExecutor struct {
	embed func(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error)
}

func (f *fakeEmbedExecutor) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return f.embed(ctx, req)
}

// interceptCall records one InterceptClass invocation, so a test can
// assert exactly what ProviderEmbedder sent to the seam.
type interceptCall struct {
	class   EgressClass
	tier    provider.SensitivityTier
	content []byte
}

// passthroughIntercept is a fake Interceptor. With err nil it returns
// content unchanged; with err set every call fails closed, never reaching
// the underlying ModelProvider.
type passthroughIntercept struct {
	calls []interceptCall
	err   error
}

func (p *passthroughIntercept) InterceptClass(_ context.Context, class EgressClass, tier provider.SensitivityTier, content []byte) ([]byte, error) {
	p.calls = append(p.calls, interceptCall{class: class, tier: tier, content: content})
	if p.err != nil {
		return nil, p.err
	}
	return content, nil
}

// testModel is the embedding space every test in this package uses: a
// 3-wide vector, small enough to hand-check positional correspondence by
// eye.
var testModel = provider.EmbedModel{ID: "test-embed-v1", Dimensions: 3}

// noopEmbed answers with one all-zero vector per input, at testModel's
// width. It is used by tests that only need Embed to succeed, not to
// prove anything about the vectors it returns.
func noopEmbed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	vectors := make([][]float32, len(req.Inputs))
	for i := range vectors {
		vectors[i] = []float32{0, 0, 0}
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

// recordedEmbedFixture returns the batch response testdata/README.md
// documents: three pairwise-distinct, testModel-wide vectors. Their
// distinctness is what makes the positional-correspondence assertion in
// TestProviderEmbedderRecordedFixture meaningful - identical vectors would
// pass even if the batch were silently reordered.
func recordedEmbedFixture() provider.ModelEmbedResponse {
	return provider.ModelEmbedResponse{
		Vectors: [][]float32{
			{0.11, 0.22, 0.33},
			{0.44, 0.55, 0.66},
			{0.77, 0.88, 0.99},
		},
		Usage: provider.Usage{InputTokens: 9},
	}
}

// newTestEmbedder builds a ProviderEmbedder over a fakeEmbedExecutor
// driven by embed, with intercept as its Interceptor (a fresh
// passthroughIntercept when nil).
func newTestEmbedder(t *testing.T, embed func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error), intercept Interceptor) *ProviderEmbedder {
	t.Helper()
	if intercept == nil {
		intercept = &passthroughIntercept{}
	}
	pe, err := NewProviderEmbedder(&fakeEmbedExecutor{embed: embed}, testModel, provider.SensitivityInternal, intercept)
	if err != nil {
		t.Fatalf("NewProviderEmbedder: %v", err)
	}
	return pe
}

// assertKind fails t unless err carries kind.
func assertKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if !ok || got != kind {
		t.Fatalf("kind = %v, ok=%v, want %v", got, ok, kind)
	}
}
