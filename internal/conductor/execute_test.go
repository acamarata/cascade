package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestExecute_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("job_id is empty on a successful dispatch")
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
}

func TestExecute_InvalidRequestRefusesBeforeDispatch(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		t.Fatal("provider called on an invalid request")
		return provider.ChatResponse{}, nil
	}
	_, err := exec.Execute(context.Background(), provider.ModelRequest{})
	if err != ErrInvalidRequest {
		t.Fatalf("got %v, want ErrInvalidRequest", err)
	}
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want 1", deps.audit.count())
	}
}

func TestExecute_NoLaneRefuses(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.router.selectFn = func(context.Context, provider.ModelRequest, ...string) (provider.Selection, error) {
		return provider.Selection{}, ErrNoLane
	}
	_, err := exec.Execute(context.Background(), validReq())
	if err != ErrNoLane {
		t.Fatalf("got %v, want ErrNoLane", err)
	}
}

func TestExecute_SensitivityFailClosed(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.audit.events) != 1 || deps.audit.events[0].RiskLevel != "restricted" {
		t.Fatalf("audit RiskLevel = %q, want restricted (zero-value sensitivity)", deps.audit.events[0].RiskLevel)
	}
}

func TestExecute_LocalOnlyRefusesExternal(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	req := validReq()
	req.Sensitivity = provider.SensitivityLocalOnly
	req.Policy.ExternalAllowed = true
	_, err := exec.Execute(context.Background(), req)
	if err != ErrSensitivityViolation {
		t.Fatalf("got %v, want ErrSensitivityViolation", err)
	}
	if deps.router.calls != 0 {
		t.Fatalf("router.Select called %d times, want 0", deps.router.calls)
	}
}

func TestExecute_TaskClassPrecedence(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	var seen string
	deps.router.selectFn = func(_ context.Context, req provider.ModelRequest, _ ...string) (provider.Selection, error) {
		seen = req.TaskClass
		return provider.Selection{LaneID: "lane-1"}, nil
	}
	req := validReq()
	req.TaskClass = "code"
	if _, err := exec.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen != "code" {
		t.Fatalf("router saw task_class %q, want %q (conductor resolves no model_class)", seen, "code")
	}
}

func TestExecute_ErrorTaxonomyMapping(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("vendor exploded")
	}
	_, err := exec.Execute(context.Background(), validReq())
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("raw provider error mapped to %v, want KindInternal", err)
	}
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, cascade.New(cascade.KindTimeout, "vendor timed out")
	}
	_, err = exec.Execute(context.Background(), validReq())
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("taxonomy provider error mapped to %v, want KindTimeout preserved", err)
	}
}

func TestExecute_AuditEventEmitted(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ev := deps.audit.events[0]
	if ev.Kind != audit.KindPolicyRoute || ev.Action != "model.execute" {
		t.Fatalf("audit event = %+v, want kind policy.route action model.execute", ev)
	}
}

func TestExecute_AuditRecordRedactedOnEveryOutcome(t *testing.T) {
	req := validReq()
	req.Inputs[0].Content = "the-secret-payload"

	exec, deps := newReadyExecutor(t)
	if _, err := exec.Execute(context.Background(), req); err != nil {
		t.Fatalf("success: %v", err)
	}

	cfg2, deps2 := newReadyConfig(t)
	cfg2.Classifier = &fakeClassifier{err: ErrInvalidRequest}
	exec2, err := NewExecutor(cfg2)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	_, _ = exec2.Execute(context.Background(), req)

	for _, spy := range []*spyAudit{deps.audit, deps2.audit} {
		if len(spy.events) != 1 {
			t.Fatalf("audit count = %d, want exactly 1", len(spy.events))
		}
		if strings.Contains(string(spy.events[0].Explain), "the-secret-payload") {
			t.Fatal("audit Explain leaked raw request content")
		}
	}
}

func TestExecute_CapabilityDenied_SingleFailover(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	chatCalls := 0
	deps.router.selectFn = func(_ context.Context, _ provider.ModelRequest, exclude ...string) (provider.Selection, error) {
		if len(exclude) == 0 {
			return provider.Selection{LaneID: "lane-1"}, nil
		}
		if exclude[0] != "lane-1" {
			t.Fatalf("exclude = %v, want [lane-1]", exclude)
		}
		return provider.Selection{LaneID: "lane-2"}, nil
	}
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		chatCalls++
		if chatCalls == 1 {
			return provider.ChatResponse{}, cascade.New(cascade.KindCapabilityDenied, "lane-1 lacks the tool")
		}
		return provider.ChatResponse{Message: provider.ChatMessage{Content: "ok"}}, nil
	}
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Selection.LaneID != "lane-2" {
		t.Fatalf("final selection = %q, want lane-2", resp.Selection.LaneID)
	}
	if chatCalls != 2 {
		t.Fatalf("dispatch called %d times, want exactly 2 (one denial, one failover)", chatCalls)
	}
	if deps.router.calls != 2 {
		t.Fatalf("router.Select called %d times, want exactly 2", deps.router.calls)
	}
}

func TestExecute_CancellationPropagation(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	block := make(chan struct{})
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		<-block
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	ch, cancel, err := exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	cancel()
	cancel() // idempotent, must not panic
	close(block)
	drained := 0
	for range ch {
		drained++ // drain without panicking on the closed channel
	}
	_ = drained
}

// TestExecute_FanOutParentResponseShape is UNMET: fan-out requires
// provider.ModelRequest.FanOut and provider.ModelResponse.Legs/CostRecord
// (R-21.214), which R-40.X8 places in pkg/provider/model.go. This ticket's
// files_scope.change is empty and does not include pkg/provider/model.go,
// so those fields cannot be added here without violating scope. See the
// journal for both sides of this contradiction quoted.
func TestExecute_FanOutParentResponseShape(t *testing.T) {
	t.Skip("blocked: provider.ModelRequest has no FanOut field and files_scope forbids editing pkg/provider/model.go to add it; see journal")
}
