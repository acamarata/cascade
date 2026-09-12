package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestChat_HappyPath proves the sel-based Chat leaf door dispatches
// through the same Resolver-backed path Execute's own dispatchWithFailover
// uses, and writes exactly one success audit record.
func TestChat_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	want := provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "hi"}}
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return want, nil
	}
	sel := provider.Selection{LaneID: "lane-1"}
	got, err := exec.Chat(context.Background(), sel, provider.ChatRequest{Model: "m1"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Message.Content != "hi" {
		t.Fatalf("Chat response = %+v, want %+v", got, want)
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
	ev := deps.audit.events[0]
	if ev.Action != "model.chat" || ev.Verdict != "success" {
		t.Fatalf("audit event = %+v, want action model.chat verdict success", ev)
	}
}

// TestChat_NotReadyMakesZeroProviderCalls proves Chat shares Execute's
// Ready() gate.
func TestChat_NotReadyMakesZeroProviderCalls(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Classifier = nil
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		t.Fatal("provider called before the security pipeline is ready")
		return provider.ChatResponse{}, nil
	}
	_, err = exec.Chat(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{})
	if err != ErrSecurityPipelineNotReady {
		t.Fatalf("got %v, want ErrSecurityPipelineNotReady", err)
	}
	if deps.audit.count() != 0 {
		t.Fatalf("audit count = %d, want 0", deps.audit.count())
	}
}

// TestChat_ErrorTaxonomyMapping proves a raw provider error is mapped to
// KindInternal exactly like Embed/Execute's own dispatch path.
func TestChat_ErrorTaxonomyMapping(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("vendor exploded")
	}
	_, err := exec.Chat(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{})
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("raw provider error mapped to %v, want KindInternal", err)
	}
	if deps.audit.events[0].Outcome != "provider_failure" {
		t.Fatalf("audit outcome = %q, want provider_failure", deps.audit.events[0].Outcome)
	}
}
