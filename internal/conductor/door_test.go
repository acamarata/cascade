package conductor

// Purpose (this file): the leaf-door admission tests - tier, lane and
//   Selection checks that run before any provider is resolved.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const remoteURL = "https://api.example.com"

// remoteLane is a lane whose provider is another machine.
func remoteLane(name string) laneCandidate {
	return laneCandidate{
		lane:     provider.LaneInfo{LaneName: name, ProviderName: "prov-" + name},
		provider: provider.ProviderInfo{Name: "prov-" + name, BaseURL: remoteURL},
	}
}

// leafCalls runs one call of every leaf verb with sel under ctx.
func leafCalls(ctx context.Context, e *Executor, sel provider.Selection) map[DoorVerb]error {
	msgs := []provider.ChatMessage{{Role: "user", Content: "hi"}}
	sink := func(provider.StreamEvent) error { return nil }
	out := map[DoorVerb]error{}
	_, out[VerbChat] = e.Chat(ctx, sel, provider.ChatRequest{Messages: msgs})
	_, out[VerbEmbed] = e.Embed(ctx, sel, provider.ModelEmbedRequest{Inputs: []string{"hi"}})
	_, out[VerbCount] = e.Count(ctx, sel, provider.CountRequest{Text: "hi"})
	out[VerbStream] = e.Stream(ctx, sel, provider.ChatRequest{Messages: msgs}, sink)
	return out
}

// countingProvider fails the test's call count if any verb is reached.
func countingProvider(deps *readyDeps) *int {
	n := new(int)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		*n++
		return provider.ChatResponse{}, nil
	}
	deps.prov.streamFn = func(context.Context, provider.ChatRequest, provider.StreamSink) error { *n++; return nil }
	deps.prov.embedFn = func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		*n++
		return provider.ModelEmbedResponse{}, nil
	}
	deps.prov.countFn = func(context.Context, provider.CountRequest) (provider.CountResponse, error) {
		*n++
		return provider.CountResponse{}, nil
	}
	return n
}

func TestLeafDoorRefusesRemoteLaneForRestricted(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.router.laneFn = func(context.Context, provider.Selection) (laneCandidate, RestrictedPermits, error) {
		return remoteLane("lane-x"), RestrictedPermits{}, nil
	}
	calls := countingProvider(deps)
	for verb, err := range leafCalls(context.Background(), exec, provider.Selection{LaneID: "lane-x"}) {
		if !errors.Is(err, ErrSensitivityViolation) {
			t.Fatalf("%s: err = %v, want ErrSensitivityViolation", verb, err)
		}
		if !strings.Contains(err.Error(), `"lane-x"`) || !strings.Contains(err.Error(), "restricted") {
			t.Errorf("%s: message %q must name the lane and the tier", verb, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("provider called %d times for a refused lane, want 0", *calls)
	}
	if deps.audit.count() != 4 {
		t.Fatalf("audit count = %d, want one refusal per verb", deps.audit.count())
	}
	for _, ev := range deps.audit.events {
		if ev.RiskLevel != "restricted" || ev.Verdict != "refusal" || !strings.HasPrefix(ev.Action, "model.") {
			t.Errorf("refusal audit = %+v, want RiskLevel restricted, verdict refusal, a model.* action", ev)
		}
	}
	// Control: the same lane is admitted once the caller declares public.
	pub := ContextWithDoorSensitivity(context.Background(), provider.SensitivityPublic)
	for verb, err := range leafCalls(pub, exec, provider.Selection{LaneID: "lane-x"}) {
		if err != nil {
			t.Errorf("%s at public: %v", verb, err)
		}
	}
}

// laneRegistry serves model m1 from two lanes and m2 from one; all local.
func laneRegistry() *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-a", BaseURL: "http://127.0.0.1:9000", KnownModels: []string{"m1", "m2"}},
			{Name: "prov-b", BaseURL: "http://127.0.0.1:9001", KnownModels: []string{"m1"}},
			{Name: "prov-r", BaseURL: remoteURL, KnownModels: []string{"mr"}},
		},
		lanes: []provider.LaneInfo{
			{LaneName: "lane-a", ProviderName: "prov-a"},
			{LaneName: "lane-b", ProviderName: "prov-b"},
			{LaneName: "lane-r", ProviderName: "prov-r"},
		},
	}
}

// selResolver records the Selection each dispatch resolved.
type selResolver struct {
	prov provider.ModelProvider
	sels []provider.Selection
}

func (r *selResolver) Resolve(_ context.Context, sel provider.Selection) (provider.ModelProvider, error) {
	r.sels = append(r.sels, sel)
	return r.prov, nil
}

func realRouterExecutor(t *testing.T) (*Executor, *selResolver) {
	t.Helper()
	cfg, deps := newReadyConfig(t)
	res := &selResolver{prov: deps.prov}
	cfg.Router, cfg.Resolver = NewRouter(laneRegistry(), &fakeQuota{}, nil, nil), res
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return exec, res
}

func TestLeafDoorEmptyLaneIDResolvesOrRefuses(t *testing.T) {
	exec, res := realRouterExecutor(t)
	chat := func(sel provider.Selection) error {
		_, err := exec.Chat(context.Background(), sel, provider.ChatRequest{})
		return err
	}
	for _, sel := range []provider.Selection{{Model: "m1"}, {}, {Model: "nope"}, {LaneID: "ghost"}} {
		if err := chat(sel); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("selection %+v: err = %v, want KindInvalidInput", sel, err)
		}
	}
	if len(res.sels) != 0 {
		t.Fatalf("resolver reached for a refused selection: %+v", res.sels)
	}
	if err := chat(provider.Selection{Model: "m2"}); err != nil {
		t.Fatalf("one lane serves m2, got %v", err)
	}
	if got := res.sels[0]; got.LaneID != "lane-a" || got.Provider != "prov-a" {
		t.Fatalf("dispatched selection = %+v, want the registry's lane-a/prov-a", got)
	}
	// The same admission filter applies to the resolved lane.
	wantViolation(t, chat(provider.Selection{Model: "mr"}), "lane-r", "restricted")
}

func TestLeafDoorDoesNotTrustTheSelection(t *testing.T) {
	exec, res := realRouterExecutor(t)
	ctx := context.Background()
	chat := func(c context.Context, sel provider.Selection) error {
		_, err := exec.Chat(c, sel, provider.ChatRequest{})
		return err
	}
	// A local LaneID labelled with a remote Provider, and the reverse.
	for _, sel := range []provider.Selection{{LaneID: "lane-a", Provider: "prov-r"}, {LaneID: "lane-r", Provider: "prov-a"}} {
		if err := chat(ctx, sel); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("selection %+v: err = %v, want KindInvalidInput", sel, err)
		}
	}
	// Public context tier, but the thread is local-only: the remote lane is refused.
	pub := ContextWithDoorSensitivity(ctx, provider.SensitivityPublic)
	if err := chat(pub, provider.Selection{LaneID: "lane-r"}); err != nil {
		t.Fatalf("control (public, no thread): %v", err)
	}
	thread := ContextWithThreadPrivacy(pub, ThreadPrivacy{ThreadID: "th", Mode: provider.SensitivityLocalOnly})
	wantViolation(t, chat(thread, provider.Selection{LaneID: "lane-r"}), "lane-r", "local-only")
	// An out-of-range tier joins to local-only, never to something wider.
	bad := ContextWithDoorSensitivity(ctx, provider.SensitivityTier(99))
	wantViolation(t, chat(bad, provider.Selection{LaneID: "lane-r"}), "lane-r", "local-only")
	if err := chat(bad, provider.Selection{LaneID: "lane-a"}); err != nil {
		t.Fatalf("invalid ctx tier on a local lane: %v", err)
	}
	if len(res.sels) != 2 {
		t.Fatalf("resolver reached %d times, want 2 (the two admitted calls)", len(res.sels))
	}
}

// bareRouter is a Router that cannot expose its lane registry.
type bareRouter struct{}

func (bareRouter) Select(context.Context, provider.ModelRequest, ...string) (provider.Selection, error) {
	return provider.Selection{LaneID: "lane-1"}, nil
}

func TestLeafDoorRefusesARouterWithoutLaneData(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Router = bareRouter{}
	calls := countingProvider(deps)
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	for verb, err := range leafCalls(context.Background(), exec, provider.Selection{LaneID: "lane-1"}) {
		if err != ErrSecurityPipelineNotReady {
			t.Errorf("%s: err = %v, want ErrSecurityPipelineNotReady", verb, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("provider called %d times, want 0", *calls)
	}
}

// narrowGate narrows every tier below local-only to the given floor.
type narrowGate struct{ to provider.SensitivityTier }

func (g narrowGate) Resolve(t provider.SensitivityTier) provider.SensitivityTier {
	return provider.JoinSensitivity(t, g.to)
}

func TestLeafDoorAppliesTheSensitivityGate(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.router.laneFn = func(context.Context, provider.Selection) (laneCandidate, RestrictedPermits, error) {
		return remoteLane("lane-x"), RestrictedPermits{}, nil
	}
	cfg, _ := newReadyConfig(t)
	cfg.Router, cfg.Sensitivity = deps.router, narrowGate{to: provider.SensitivityRestricted}
	gated, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	pub := ContextWithDoorSensitivity(context.Background(), provider.SensitivityPublic)
	if _, err := exec.Chat(pub, provider.Selection{LaneID: "lane-x"}, provider.ChatRequest{}); err != nil {
		t.Fatalf("control (passthrough gate): %v", err)
	}
	_, err = gated.Chat(pub, provider.Selection{LaneID: "lane-x"}, provider.ChatRequest{})
	wantViolation(t, err, "lane-x", "restricted")
}
