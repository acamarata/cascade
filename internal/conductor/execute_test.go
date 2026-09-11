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

// TestExecute_FanOutParentResponseShape is MET (T0 unblock,
// P1-E11-W3-S22-T1/S23-T2): Executor.ExecuteFanOut (execute.go) routes n
// legs through FanOut (fanout.go), using e.Execute as each leg's exec
// function, and returns one index-ordered ModelResponse per leg, each
// with its own real JobID from a real (audited) Execute call.
// TestExecute_FanOutParentResponseShape_Assembled (below) covers the full
// R-21.214 parent shape (parent JobID, empty Output, summed Usage,
// index-ordered Legs) that provider.ModelResponse.Legs now carries.
// concurrencySafeRouter wraps a *fakeRouter's fixed-mapping behavior
// without its unsynchronized calls counter (model_test.go's fakeRouter is
// shared across every _test.go file in this package and is not safe for
// concurrent Select calls - the first caller to dispatch concurrent legs
// through it, so this test supplies its own thread-safe double instead of
// editing model_test.go, which is outside this ticket's files_scope).
type concurrencySafeRouter struct{}

func (concurrencySafeRouter) Select(_ context.Context, _ provider.ModelRequest, _ ...string) (provider.Selection, error) {
	return provider.Selection{LaneID: "lane-1", Provider: "test", Model: "test-model"}, nil
}

func TestExecute_FanOutParentResponseShape(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Router = concurrencySafeRouter{}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	req := validReq()
	results, err := exec.ExecuteFanOut(context.Background(), req, 2, nil, passthroughPermit, &spyJournal{})
	if err != nil {
		t.Fatalf("ExecuteFanOut: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for i, r := range results {
		if r.JobID == "" {
			t.Errorf("leg %d: empty JobID", i)
		}
	}
	if deps.audit.count() != 2 {
		t.Fatalf("audit count = %d, want 2 (one per leg through Execute)", deps.audit.count())
	}
}

// TestExecute_FanOutParentResponseShape_Assembled asserts
// ExecuteFanOutResponse's R-21.214 parent shape: a parent JobID distinct
// from every leg's own JobID, empty parent Output, Legs holding the same
// index-ordered leg responses ExecuteFanOut returns, and parent Usage
// equal to the sum of every leg's Usage.
func TestExecute_FanOutParentResponseShape_Assembled(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	cfg.Router = concurrencySafeRouter{}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	req := validReq()
	parent, err := exec.ExecuteFanOutResponse(context.Background(), req, 3, nil, passthroughPermit, &spyJournal{})
	if err != nil {
		t.Fatalf("ExecuteFanOutResponse: %v", err)
	}
	if parent.JobID == "" {
		t.Fatal("parent JobID is empty")
	}
	if parent.Output != "" {
		t.Errorf("parent Output = %q, want empty", parent.Output)
	}
	if len(parent.Legs) != 3 {
		t.Fatalf("len(parent.Legs) = %d, want 3", len(parent.Legs))
	}
	var wantInput, wantOutput int
	for i, leg := range parent.Legs {
		if leg.JobID == "" {
			t.Errorf("leg %d: empty JobID", i)
		}
		if leg.JobID == parent.JobID {
			t.Errorf("leg %d: JobID collides with parent JobID", i)
		}
		wantInput += leg.Usage.InputTokens
		wantOutput += leg.Usage.OutputTokens
	}
	if parent.Usage.InputTokens != wantInput || parent.Usage.OutputTokens != wantOutput {
		t.Errorf("parent Usage = %+v, want summed {%d %d}", parent.Usage, wantInput, wantOutput)
	}
}
