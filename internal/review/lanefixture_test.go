package review

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the fixtures router_test.go routes through (P1-E25-W5-S52-T4, CR
//   fix D1): a lane registry whose lanes are EXTERNAL (a resolvable host
//   elsewhere) or controller-local, a taxonomy-order spiller, the real
//   conductor.DefaultRouter, and routerExecutor -- the conductor-boundary
//   double that SELECTS through the real router before it dispatches, so a
//   refusal can be told apart from a provider call. Copied in shape from
//   internal/context/pipeline_lanefixture_test.go, which is the established
//   real-router proof pattern in this tree.
// Constraints: Art.2 -- the registry, the spiller and the clock are doubles;
//   the ROUTER, its five filters (FILTER 0 privacy included) and the §5.16
//   taxonomy table are REAL. Nothing here opens a socket.
// SPORT: internal/review.lane-fixture (ADD, P1-E25-W5-S52-T4).

const (
	externalLaneName = "lane-external"
	localLaneName    = "lane-controller-local"
)

// laneRegistry is a provider.ProviderRegistryReader double carrying whatever
// providers and lanes a test needs. BaseURL is what conductor.ClassifyLane
// reads to decide controller-local vs external-api, so the two lanes below
// differ in exactly that.
type laneRegistry struct {
	providers []provider.ProviderInfo
	lanes     []provider.LaneInfo
}

// externalOnlyRegistry offers ONE lane, on a resolvable host elsewhere: the
// registry shape a local-only thread must be refused against.
func externalOnlyRegistry() *laneRegistry {
	return &laneRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-external", Driver: "anthropic", BaseURL: "https://api.example.com",
				KnownModels: []string{"model-external"}, HealthStatus: "healthy"},
			{Name: "prov-external-2", Driver: "openai-compat", BaseURL: "https://api2.example.com",
				KnownModels: []string{"model-external-2"}, HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: externalLaneName, ProviderName: "prov-external", State: "active"},
			{LaneName: externalLaneName + "-2", ProviderName: "prov-external-2", State: "active"},
		},
	}
}

// withLocalLane adds a controller-local lane (loopback BaseURL), the only
// lane a local-only thread may use.
func (r *laneRegistry) withLocalLane() *laneRegistry {
	r.providers = append(r.providers, provider.ProviderInfo{
		Name: "prov-local", Driver: "local-compat", BaseURL: "http://127.0.0.1:9099",
		KnownModels: []string{"model-local"}, HealthStatus: "healthy",
	})
	r.lanes = append(r.lanes, provider.LaneInfo{
		LaneName: localLaneName, ProviderName: "prov-local", State: "active",
	})
	return r
}

func (r *laneRegistry) GetProvider(_ context.Context, name string) (provider.ProviderInfo, error) {
	for _, p := range r.providers {
		if p.Name == name {
			return p, nil
		}
	}
	return provider.ProviderInfo{}, cascade.New(cascade.KindNotFound, "laneRegistry: unknown provider")
}

func (r *laneRegistry) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	return r.providers, nil
}

func (r *laneRegistry) ListLanes(_ context.Context) ([]provider.LaneInfo, error) { return r.lanes, nil }

func (r *laneRegistry) ListPool(_ context.Context, pool string) ([]provider.LaneInfo, error) {
	var out []provider.LaneInfo
	for _, l := range r.lanes {
		if l.PoolMembership == pool {
			out = append(out, l)
		}
	}
	return out, nil
}

func (r *laneRegistry) GetByModel(_ context.Context, model string) ([]provider.ProviderInfo, error) {
	var out []provider.ProviderInfo
	for _, p := range r.providers {
		for _, m := range p.KnownModels {
			if m == model {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// laneOrderSpiller is a conductor.QuotaSpiller double offering lanes in one
// configured order, exhausting with a typed error like the real policy does.
type laneOrderSpiller struct{ order []conductor.LaneID }

func (s *laneOrderSpiller) NextLane(_ context.Context, excluded []conductor.LaneID) (conductor.LaneID, error) {
	barred := make(map[conductor.LaneID]bool, len(excluded))
	for _, x := range excluded {
		barred[x] = true
	}
	for _, l := range s.order {
		if !barred[l] {
			return l, nil
		}
	}
	return "", cascade.New(cascade.KindQuotaExhausted, "laneOrderSpiller: every lane excluded")
}

// realRouter builds a REAL conductor.DefaultRouter over reg and the REAL
// §5.16 taxonomy table, spilling across every lane reg declares.
func realRouter(t *testing.T, reg *laneRegistry) *conductor.DefaultRouter {
	t.Helper()
	order := make([]conductor.LaneID, 0, len(reg.lanes))
	for _, l := range reg.lanes {
		order = append(order, conductor.LaneID(l.LaneName))
	}
	return conductor.NewRouter(reg, &laneOrderSpiller{order: order},
		testkit.NewFrozenClock(time.Unix(0, 0)), conductor.TaskClasses())
}

// routerExecutor is the conductor-boundary double: it does what the real
// Executor does around a Select -- refuse when no lane survives, dispatch to
// the one that did -- and records both counts separately, so "the router
// refused" is distinguishable from "a provider was called". It keeps the ctx
// it was handed, so a caller that stripped the thread privacy is visible.
type routerExecutor struct {
	router  *conductor.DefaultRouter
	outputs []string

	selects    int
	dispatches int
	lanes      []string
	requests   []provider.ModelRequest
}

func (e *routerExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	e.selects++
	e.requests = append(e.requests, req)
	sel, err := e.router.Select(ctx, req)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	out := `{"approved":true,"findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`
	if i := e.dispatches; i < len(e.outputs) {
		out = e.outputs[i]
	}
	e.dispatches++
	e.lanes = append(e.lanes, sel.LaneID)
	return provider.ModelResponse{Output: out, Selection: provider.Selection{
		LaneID: sel.LaneID, Provider: sel.Provider, Model: sel.Model,
	}}, nil
}

// localOnlyThreadContext returns a ctx carrying a local-only thread privacy
// mode, exactly as internal/conversation's dispatch path sets it.
func localOnlyThreadContext() context.Context {
	return conductor.ContextWithThreadPrivacy(context.Background(), conductor.ThreadPrivacy{
		ThreadID: "thread-local-only-fixture",
		Mode:     provider.SensitivityLocalOnly,
	})
}
