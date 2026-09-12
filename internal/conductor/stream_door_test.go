package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestStreamDoor_HappyPath proves the sel-based Stream leaf door dispatches
// through the Resolver-backed path, delivers events to sink, and writes
// exactly one success audit record.
func TestStreamDoor_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	var delivered []provider.StreamEvent
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hi"}); err != nil {
			return err
		}
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	sel := provider.Selection{LaneID: "lane-1"}
	err := exec.Stream(context.Background(), sel, provider.ChatRequest{Model: "m1"}, func(ev provider.StreamEvent) error {
		delivered = append(delivered, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(delivered) != 2 || delivered[0].Delta != "hi" {
		t.Fatalf("delivered = %+v, want [delta(hi), done]", delivered)
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
	ev := deps.audit.events[0]
	if ev.Action != "model.stream" || ev.Verdict != "success" {
		t.Fatalf("audit event = %+v, want action model.stream verdict success", ev)
	}
}

// TestStreamDoor_NotReadyMakesZeroProviderCalls proves Stream shares
// Execute's Ready() gate.
func TestStreamDoor_NotReadyMakesZeroProviderCalls(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Classifier = nil
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	deps.prov.streamFn = func(context.Context, provider.ChatRequest, provider.StreamSink) error {
		t.Fatal("provider called before the security pipeline is ready")
		return nil
	}
	err = exec.Stream(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{}, func(provider.StreamEvent) error { return nil })
	if err != ErrSecurityPipelineNotReady {
		t.Fatalf("got %v, want ErrSecurityPipelineNotReady", err)
	}
	if deps.audit.count() != 0 {
		t.Fatalf("audit count = %d, want 0", deps.audit.count())
	}
}

// TestStreamDoor_ErrorTaxonomyMapping proves a raw provider error is
// mapped to KindInternal exactly like Chat/Embed/Execute's own dispatch
// path.
func TestStreamDoor_ErrorTaxonomyMapping(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.streamFn = func(context.Context, provider.ChatRequest, provider.StreamSink) error {
		return errors.New("vendor exploded")
	}
	err := exec.Stream(context.Background(), provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{}, func(provider.StreamEvent) error { return nil })
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("raw provider error mapped to %v, want KindInternal", err)
	}
	if deps.audit.events[0].Outcome != "provider_failure" {
		t.Fatalf("audit outcome = %q, want provider_failure", deps.audit.events[0].Outcome)
	}
}
