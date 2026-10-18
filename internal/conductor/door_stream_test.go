package conductor

// Purpose (this file): the streaming half of the door - runStream failover
//   and usage attribution, the cancellation cause in the audit record, the
//   Execute failover keeping attempt-1 usage, and the router seeing the
//   resolved tier.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recUsage is a UsageRecorder that keeps every record.
type recUsage struct {
	mu   sync.Mutex
	recs []UsageRecord
}

func (r *recUsage) WriteUsageRecord(_ context.Context, rec UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}

// laneResolver serves a different provider per lane.
type laneResolver map[string]provider.ModelProvider

func (r laneResolver) Resolve(_ context.Context, sel provider.Selection) (provider.ModelProvider, error) {
	return r[sel.LaneID], nil
}

var errDenied = cascade.New(cascade.KindCapabilityDenied, "lane lacks the capability")

// failoverRig wires two lanes: lane-1 first, lane-2 once lane-1 is excluded.
func failoverRig(t *testing.T) (*Executor, *readyDeps, *recUsage, map[string]*fakeProvider) {
	t.Helper()
	cfg, deps := newReadyConfig(t)
	provs := map[string]*fakeProvider{"lane-1": {}, "lane-2": {}}
	deps.router.selectFn = func(_ context.Context, _ provider.ModelRequest, exclude ...string) (provider.Selection, error) {
		if slices.Contains(exclude, "lane-1") {
			return provider.Selection{LaneID: "lane-2", Provider: "test"}, nil
		}
		return provider.Selection{LaneID: "lane-1", Provider: "test"}, nil
	}
	cfg.Resolver = laneResolver{"lane-1": provs["lane-1"], "lane-2": provs["lane-2"]}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	usage := &recUsage{}
	exec.SetUsageStore(usage)
	return exec, deps, usage, provs
}

func collect(ch <-chan provider.StreamEvent) []provider.StreamEvent {
	var out []provider.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestRunStreamFailoverAndUsage(t *testing.T) {
	exec, deps, usage, provs := failoverRig(t)
	calls := map[string]int{}
	provs["lane-1"].streamFn = func(context.Context, provider.ChatRequest, provider.StreamSink) error {
		calls["lane-1"]++
		return errDenied
	}
	provs["lane-2"].streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		calls["lane-2"]++
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hi"})
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone, Usage: provider.Usage{InputTokens: 3, OutputTokens: 4}})
	}
	ch, _, err := exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	evs := collect(ch)
	if len(evs) != 2 || evs[0].Delta != "hi" {
		t.Fatalf("events = %+v, want delta(hi) then done from lane-2", evs)
	}
	if calls["lane-1"] != 1 || calls["lane-2"] != 1 || deps.router.calls != 2 {
		t.Fatalf("lane-1 %d, lane-2 %d, Select %d; want 1, 1, 2 (exactly one failover)", calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
	if len(usage.recs) != 2 {
		t.Fatalf("usage records = %+v, want one per attempt", usage.recs)
	}
	first, second := usage.recs[0], usage.recs[1]
	if first.Attempt != 1 || first.LaneID != "lane-1" || second.Attempt != 2 || second.LaneID != "lane-2" ||
		second.TokensIn != 3 || second.TokensOut != 4 || second.JobID == "" || first.JobID != attemptJobID(second.JobID, 1) {
		t.Fatalf("usage = %+v / %+v, want attempt 1 on lane-1 then attempt 2 on lane-2 (3/4 tokens), the abandoned attempt keyed off the job id", first, second)
	}
	if deps.audit.count() != 1 || deps.audit.events[0].Outcome != "success" {
		t.Fatalf("audit = %+v, want one success record", deps.audit.events)
	}
}

func TestRunStreamFailsOverAtMostOnceAndNeverAfterOutput(t *testing.T) {
	exec, deps, _, provs := failoverRig(t)
	denied := func(context.Context, provider.ChatRequest, provider.StreamSink) error { return errDenied }
	provs["lane-1"].streamFn, provs["lane-2"].streamFn = denied, denied
	ch, _, _ := exec.ExecuteStream(context.Background(), validReq())
	evs := collect(ch)
	if deps.router.calls != 2 {
		t.Fatalf("Select called %d times, want 2: a second denial must not fail over again", deps.router.calls)
	}
	if len(evs) != 1 || evs[0].Kind != provider.StreamEventError {
		t.Fatalf("events = %+v, want the one terminal error event", evs)
	}
	if got := deps.audit.events[len(deps.audit.events)-1]; got.Outcome != "provider_failure" {
		t.Fatalf("outcome = %q, want provider_failure", got.Outcome)
	}

	exec, deps, _, provs = failoverRig(t)
	provs["lane-1"].streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "partial"})
		return errDenied
	}
	ch, _, _ = exec.ExecuteStream(context.Background(), validReq())
	collect(ch)
	if deps.router.calls != 1 {
		t.Fatalf("Select called %d times, want 1: output already reached the caller", deps.router.calls)
	}
}

func TestCancelledStreamAuditCarriesTheCause(t *testing.T) {
	block := func(started chan<- struct{}) func(context.Context, provider.ChatRequest, provider.StreamSink) error {
		return func(ctx context.Context, _ provider.ChatRequest, _ provider.StreamSink) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
	}
	reason := func(deps *readyDeps) string {
		ev := deps.audit.events[len(deps.audit.events)-1]
		if ev.Outcome != "cancellation" {
			t.Fatalf("outcome = %q, want cancellation", ev.Outcome)
		}
		return string(ev.Explain)
	}

	exec, deps, _, provs := failoverRig(t)
	started := make(chan struct{})
	provs["lane-1"].streamFn = block(started)
	ch, cancel, _ := exec.ExecuteStream(context.Background(), validReq())
	<-started
	cancel()
	collect(ch)
	if got := reason(deps); !strings.Contains(got, errJobCancelled.Error()) {
		t.Fatalf("caller cancel: explain %s lacks the cause %q", got, errJobCancelled)
	}

	exec, deps, _, provs = failoverRig(t)
	started = make(chan struct{})
	provs["lane-1"].streamFn = block(started)
	parent, stop := context.WithCancelCause(context.Background())
	ch, _, _ = exec.ExecuteStream(parent, validReq())
	<-started
	stop(errors.New("operator shutdown"))
	collect(ch)
	if got := reason(deps); !strings.Contains(got, "operator shutdown") {
		t.Fatalf("parent cancel: explain %s lacks the parent's cause", got)
	}
}

func TestExecuteFailoverKeepsAttempt1Usage(t *testing.T) {
	exec, _, usage, provs := failoverRig(t)
	provs["lane-1"].chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 9}}, errDenied
	}
	provs["lane-2"].chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}}, nil
	}
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(usage.recs) != 2 || usage.recs[0].Attempt != 1 || usage.recs[0].TokensIn != 9 || usage.recs[1].Attempt != 2 {
		t.Fatalf("usage = %+v, want attempt 1 (9 tokens in) kept alongside attempt 2", usage.recs)
	}
}

func TestRouterSeesResolvedTier(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	var seen provider.SensitivityTier
	deps.router.selectFn = func(_ context.Context, req provider.ModelRequest, _ ...string) (provider.Selection, error) {
		seen = req.Sensitivity
		return provider.Selection{LaneID: "lane-1"}, nil
	}
	cfg.Sensitivity = narrowGate{to: provider.SensitivityRestricted}
	exec, _ := NewExecutor(cfg)
	req := validReq()
	req.Sensitivity = provider.SensitivityPublic
	if _, err := exec.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen != provider.SensitivityRestricted {
		t.Fatalf("Router.Select saw %s, want the resolved restricted", seen)
	}

	// Through the real router: public would reach the remote lane, restricted must not.
	cfg, deps = newReadyConfig(t)
	cfg.Router = NewRouter(restrictedRegistry(remoteURL), &fakeQuota{order: []LaneID{"lane-remote", "lane-local"}}, nil, nil)
	cfg.Resolver = &fakeResolver{provider: deps.prov}
	passthrough, _ := NewExecutor(cfg)
	cfg.Sensitivity = narrowGate{to: provider.SensitivityRestricted}
	narrowed, _ := NewExecutor(cfg)
	if out, err := passthrough.Execute(context.Background(), req); err != nil || out.Selection.LaneID != "lane-remote" {
		t.Fatalf("control: public reaches the remote lane first, got %+v, %v", out.Selection, err)
	}
	if out, err := narrowed.Execute(context.Background(), req); err != nil || out.Selection.LaneID != "lane-local" {
		t.Fatalf("gate-narrowed request selected %+v, %v; want the local lane", out.Selection, err)
	}
}
