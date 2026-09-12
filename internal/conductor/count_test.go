package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestCount_HappyPath proves the sel-based Count leaf door dispatches
// through the Resolver-backed path and writes exactly one success audit
// record.
func TestCount_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	want := provider.CountResponse{Tokens: 7}
	deps.prov.countFn = func(context.Context, provider.CountRequest) (provider.CountResponse, error) {
		return want, nil
	}
	sel := provider.Selection{LaneID: "lane-1"}
	got, err := exec.Count(context.Background(), sel, provider.CountRequest{Text: "hello", Model: "m1"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got.Tokens != 7 {
		t.Fatalf("Count response = %+v, want %+v", got, want)
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
	ev := deps.audit.events[0]
	if ev.Action != "model.count" || ev.Verdict != "success" {
		t.Fatalf("audit event = %+v, want action model.count verdict success", ev)
	}
}

// TestCount_NotReadyMakesZeroProviderCalls proves Count shares Execute's
// Ready() gate.
func TestCount_NotReadyMakesZeroProviderCalls(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Classifier = nil
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	deps.prov.countFn = func(context.Context, provider.CountRequest) (provider.CountResponse, error) {
		t.Fatal("provider called before the security pipeline is ready")
		return provider.CountResponse{}, nil
	}
	_, err = exec.Count(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.CountRequest{Text: "hi"})
	if err != ErrSecurityPipelineNotReady {
		t.Fatalf("got %v, want ErrSecurityPipelineNotReady", err)
	}
	if deps.audit.count() != 0 {
		t.Fatalf("audit count = %d, want 0", deps.audit.count())
	}
}

// TestCount_ErrorTaxonomyMapping proves a raw provider error is mapped to
// KindInternal exactly like Chat/Embed/Execute's own dispatch path.
func TestCount_ErrorTaxonomyMapping(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.countFn = func(context.Context, provider.CountRequest) (provider.CountResponse, error) {
		return provider.CountResponse{}, errors.New("vendor exploded")
	}
	_, err := exec.Count(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.CountRequest{Text: "hi"})
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("raw provider error mapped to %v, want KindInternal", err)
	}
	if deps.audit.events[0].Outcome != "provider_failure" {
		t.Fatalf("audit outcome = %q, want provider_failure", deps.audit.events[0].Outcome)
	}
}
