package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestEmbed_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	want := provider.ModelEmbedResponse{Vectors: [][]float32{{0.1, 0.2}}, Usage: provider.Usage{InputTokens: 3}}
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return want, nil
	}
	sel := provider.Selection{LaneID: "lane-1"}
	got, err := exec.Embed(context.Background(), sel, provider.ModelEmbedRequest{Inputs: []string{"a"}, Model: "m1"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got.Vectors) != 1 || got.Vectors[0][0] != 0.1 {
		t.Fatalf("Embed response = %+v, want %+v", got, want)
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
	ev := deps.audit.events[0]
	if ev.Action != "model.embed" || ev.Verdict != "success" {
		t.Fatalf("audit event = %+v, want action model.embed verdict success", ev)
	}
}

// TestEmbed_NotReadyMakesZeroProviderCalls proves Embed shares Execute's
// Ready() gate: no dispatch and no audit record until all six R-21.206
// collaborators are installed.
func TestEmbed_NotReadyMakesZeroProviderCalls(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Classifier = nil
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		t.Fatal("provider called before the security pipeline is ready")
		return provider.ModelEmbedResponse{}, nil
	}
	_, err = exec.Embed(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ModelEmbedRequest{Inputs: []string{"a"}})
	if err != ErrSecurityPipelineNotReady {
		t.Fatalf("got %v, want ErrSecurityPipelineNotReady", err)
	}
	if deps.audit.count() != 0 {
		t.Fatalf("audit count = %d, want 0", deps.audit.count())
	}
}

// TestEmbed_ErrorTaxonomyMapping proves a raw provider error is mapped to
// KindInternal exactly like Execute's own dispatch path, while an
// already-typed taxonomy error passes through unchanged.
func TestEmbed_ErrorTaxonomyMapping(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return provider.ModelEmbedResponse{}, errors.New("vendor exploded")
	}
	_, err := exec.Embed(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ModelEmbedRequest{Inputs: []string{"a"}})
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("raw provider error mapped to %v, want KindInternal", err)
	}
	if deps.audit.events[0].Outcome != "provider_failure" {
		t.Fatalf("audit outcome = %q, want provider_failure", deps.audit.events[0].Outcome)
	}
}

// TestEmbed_AuditNeverLeaksInputText proves the embed batch's own text
// never reaches the audit Explain payload: only the lane id and model
// name are hashed into ParamsHash, and ExecutionTrace carries neither.
func TestEmbed_AuditNeverLeaksInputText(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return provider.ModelEmbedResponse{Vectors: [][]float32{{0}}}, nil
	}
	req := provider.ModelEmbedRequest{Inputs: []string{"the-secret-embed-payload"}, Model: "m1"}
	if _, err := exec.Embed(context.Background(), provider.Selection{LaneID: "lane-1"}, req); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if strings.Contains(string(deps.audit.events[0].Explain), "the-secret-embed-payload") {
		t.Fatal("audit Explain leaked raw embed input text")
	}
}

// TestEmbeddingExecutor_BindsSelectionOnce proves Executor.EmbeddingExecutor
// returns a value that dispatches through the SAME sole door as a direct
// Embed call, with sel bound once rather than threaded per call - the
// shape providers/embeddings.EmbedExecutor's single Embed(ctx, req) method
// expects.
func TestEmbeddingExecutor_BindsSelectionOnce(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	var gotSel provider.Selection
	deps.resolver.provider = deps.prov
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		return provider.ModelEmbedResponse{Vectors: [][]float32{{0}}}, nil
	}
	deps.router.selectFn = func(context.Context, provider.ModelRequest, ...string) (provider.Selection, error) {
		gotSel = provider.Selection{LaneID: "unused"}
		return gotSel, nil
	}
	bound := exec.EmbeddingExecutor(provider.Selection{LaneID: "fixed-lane"})
	resp, err := bound.Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"a"}})
	if err != nil {
		t.Fatalf("EmbeddingExecutor.Embed: %v", err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("resp = %+v, want one vector", resp)
	}
	if deps.audit.events[0].RiskLevel != "" {
		t.Fatalf("Embed's audit RiskLevel = %q, want empty (no ModelRequest sensitivity resolved)", deps.audit.events[0].RiskLevel)
	}
	// Router.Select is never called by the embed path: sel is bound once
	// at EmbeddingExecutor construction, never re-selected per call.
	if deps.router.calls != 0 {
		t.Fatalf("router.Select called %d times, want 0 (embed uses its bound Selection, not routing)", deps.router.calls)
	}
}
