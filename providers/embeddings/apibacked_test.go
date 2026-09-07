// Purpose: the no-network unit lane for ProviderEmbedder (apibacked.go):
//
//	replays the fixture testdata/README.md documents through a fake
//	provider.ModelProvider and a fake Interceptor (doubles_test.go),
//	proving the happy-path round-trip in order, the capability-gate
//	refusal propagation, the malformed-response error paths, and
//	sensitivity-tier propagation through the Interceptor seam.
//
// Inputs: none external - every dependency is a fake constructed in
//
//	doubles_test.go.
//
// Outputs: none - this file is tests only.
// Constraints: imports neither "net" nor "net/http" (Art.7.2's default
//
//	unit lane); the tagged live lane lives in integration_test.go.
//
// SPORT: providers.embeddings.ProviderEmbedder/ADD (P1-E10-W3-S19-T6).
package embeddings

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestProviderEmbedderRecordedFixture replays recordedEmbedFixture through
// ProviderEmbedder end to end, proving the happy-path round-trip AND
// - the part most reorder bugs slip past - that outputs land at the SAME
// index as the input that produced them, using three inputs whose
// expected vectors all differ (Embed's positional-correspondence
// contract, pkg/provider/embedder.go).
func TestProviderEmbedderRecordedFixture(t *testing.T) {
	fixture := recordedEmbedFixture()
	var gotReq provider.ModelEmbedRequest
	pe := newTestEmbedder(t, func(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		gotReq = req
		return fixture, nil
	}, nil)

	if pe.Model() != testModel {
		t.Fatalf("Model() = %+v, want %+v", pe.Model(), testModel)
	}

	inputs := []provider.EmbedInput{{Text: "alpha"}, {Text: "beta"}, {Text: "gamma"}}
	outputs, err := pe.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(outputs) != len(inputs) {
		t.Fatalf("len(outputs) = %d, want %d", len(outputs), len(inputs))
	}
	for i, out := range outputs {
		if !out.Model.Equal(testModel) {
			t.Fatalf("outputs[%d].Model = %+v, want %+v", i, out.Model, testModel)
		}
		if len(out.Vector) != testModel.Dimensions {
			t.Fatalf("outputs[%d] vector width = %d, want %d", i, len(out.Vector), testModel.Dimensions)
		}
		if out.Vector[0] != fixture.Vectors[i][0] {
			t.Fatalf("outputs[%d] = %v, does not positionally match fixture[%d] = %v", i, out.Vector, i, fixture.Vectors[i])
		}
	}
	if len(gotReq.Inputs) != 3 || gotReq.Inputs[0] != "alpha" || gotReq.Inputs[1] != "beta" || gotReq.Inputs[2] != "gamma" {
		t.Fatalf("request inputs = %v, want order-preserved [alpha beta gamma]", gotReq.Inputs)
	}
}

// TestProviderEmbedderEmptyBatch proves an empty batch is answered
// locally, per Embed's contract, without ever calling the underlying
// ModelProvider.
func TestProviderEmbedderEmptyBatch(t *testing.T) {
	pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		t.Fatal("ModelProvider.Embed must not be called for an empty batch")
		return provider.ModelEmbedResponse{}, nil
	}, nil)
	outputs, err := pe.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(outputs) != 0 {
		t.Fatalf("outputs = %+v, want empty", outputs)
	}
}

// TestProviderEmbedderCapabilityGate proves the behaviour this ticket
// names as most likely to be got wrong: anthropic's real, landed Embed
// (providers/anthropic/anthropic.go) returns cascade.KindUnsupported
// because it has no embeddings endpoint. ProviderEmbedder must propagate
// that refusal completely unchanged - never an empty slice standing in
// for "no vectors", never a zero vector, never a different Kind.
func TestProviderEmbedderCapabilityGate(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnsupported, "anthropic: the anthropic driver has no embeddings endpoint")
	pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return provider.ModelEmbedResponse{}, wantErr
	}, nil)

	outputs, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "x"}})
	if outputs != nil {
		t.Fatalf("outputs = %+v, want nil on refusal", outputs)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Embed error %v does not match the underlying refusal %v", err, wantErr)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("kind = %v, ok=%v, want KindUnsupported", kind, ok)
	}
}

// TestProviderEmbedderMalformedResponse covers the two independent
// dimension-consistency failures Embed must catch: a vector count that
// does not match the input count, and a vector whose width does not match
// the configured model - plus the zero-vectors-for-a-nonempty-batch case,
// which is the count-mismatch check at its simplest.
func TestProviderEmbedderMalformedResponse(t *testing.T) {
	t.Run("vector count mismatch", func(t *testing.T) {
		pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
			return provider.ModelEmbedResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}}, nil
		}, nil)
		_, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "a"}, {Text: "b"}})
		assertKind(t, err, cascade.KindIntegrity)
	})
	t.Run("wrong vector width", func(t *testing.T) {
		pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
			return provider.ModelEmbedResponse{Vectors: [][]float32{{0.1, 0.2}}}, nil
		}, nil)
		_, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "a"}})
		assertKind(t, err, cascade.KindIntegrity)
	})
	t.Run("empty vector response for a nonempty batch", func(t *testing.T) {
		pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
			return provider.ModelEmbedResponse{}, nil
		}, nil)
		_, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "a"}})
		assertKind(t, err, cascade.KindIntegrity)
	})
}

// TestProviderEmbedderSensitivityPropagation proves every input transits
// Interceptor under EgressClassConductor with this ProviderEmbedder's
// tier unwidened (R-21.265), and that the raw input text is what the seam
// receives.
func TestProviderEmbedderSensitivityPropagation(t *testing.T) {
	intercept := &passthroughIntercept{}
	pe, err := NewProviderEmbedder(&fakeModelProvider{embed: noopEmbed}, testModel, provider.SensitivityInternal, intercept)
	if err != nil {
		t.Fatalf("NewProviderEmbedder: %v", err)
	}
	if _, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "secret text"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(intercept.calls) != 1 {
		t.Fatalf("intercept calls = %d, want 1", len(intercept.calls))
	}
	call := intercept.calls[0]
	if call.class != EgressClassConductor {
		t.Fatalf("class = %q, want %q", call.class, EgressClassConductor)
	}
	if call.tier != provider.SensitivityInternal {
		t.Fatalf("tier = %v, want SensitivityInternal (unwidened)", call.tier)
	}
	if string(call.content) != "secret text" {
		t.Fatalf("content = %q, want the raw input text", call.content)
	}
}

// TestProviderEmbedderSeamFailsClosed proves a seam error stops the call
// before the underlying ModelProvider is ever reached, and the seam's own
// Kind is what the caller sees.
func TestProviderEmbedderSeamFailsClosed(t *testing.T) {
	seamErr := cascade.New(cascade.KindPolicyDenied, "egress: class disabled")
	intercept := &passthroughIntercept{err: seamErr}
	pe := newTestEmbedder(t, func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		t.Fatal("ModelProvider.Embed must not be called when the Interceptor seam fails closed")
		return provider.ModelEmbedResponse{}, nil
	}, intercept)
	_, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "x"}})
	if !errors.Is(err, seamErr) {
		t.Fatalf("Embed error %v does not match the seam refusal %v", err, seamErr)
	}
}

// TestNewProviderEmbedderUnresolvableTierFailsClosed proves an out-of-range
// SensitivityTier resolves to SensitivityRestricted at construction (06 §5
// rule 15), rather than being rejected or passed through unresolved.
func TestNewProviderEmbedderUnresolvableTierFailsClosed(t *testing.T) {
	intercept := &passthroughIntercept{}
	pe, err := NewProviderEmbedder(&fakeModelProvider{embed: noopEmbed}, testModel, provider.SensitivityTier(99), intercept)
	if err != nil {
		t.Fatalf("NewProviderEmbedder: %v", err)
	}
	if _, err := pe.Embed(context.Background(), []provider.EmbedInput{{Text: "x"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(intercept.calls) != 1 || intercept.calls[0].tier != provider.SensitivityRestricted {
		t.Fatalf("tier = %v, want SensitivityRestricted for an unresolvable construction-time tier", intercept.calls[0].tier)
	}
}

// TestNewProviderEmbedderValidation proves every required dependency is
// checked at construction, each with cascade.KindInvalidInput, rather than
// deferred to the first Embed call.
func TestNewProviderEmbedderValidation(t *testing.T) {
	okMP := &fakeModelProvider{embed: noopEmbed}
	okIntercept := &passthroughIntercept{}
	cases := []struct {
		name      string
		mp        provider.ModelProvider
		model     provider.EmbedModel
		intercept Interceptor
	}{
		{"nil provider", nil, testModel, okIntercept},
		{"empty model id", okMP, provider.EmbedModel{ID: "", Dimensions: 3}, okIntercept},
		{"non-positive dimensions", okMP, provider.EmbedModel{ID: "m", Dimensions: 0}, okIntercept},
		{"nil intercept", okMP, testModel, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewProviderEmbedder(c.mp, c.model, provider.SensitivityInternal, c.intercept)
			assertKind(t, err, cascade.KindInvalidInput)
		})
	}
}
