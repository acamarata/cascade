// Purpose: the Router - the conductor's sole lane-selection entry point
//   (R-40.X11). Select applies five sequential filters (capability,
//   sensitivity, health, quota/spill, cost) over ONE immutable metadata
//   snapshot taken at entry (R-21.213), and returns the pkg/provider SDK
//   Selection type the frozen Router interface (S-22.T1's model.go)
//   already declares.
// Inputs: a provider.ModelRequest and a variadic exclude list of lane ids
//   barred from this selection (the single R-14.88 capability-denied
//   failover, applied by execute.go).
// Outputs: a provider.Selection naming the chosen lane, or a taxonomy
//   error from the frozen 14-kind set.
// Constraints: no filter re-reads the registry, the eviction state or the
//   quota policy mid-pass; every read for one Select call comes from the
//   single routeSnapshot built at entry. See this package's journal for
//   the CONTRACT DEVIATION on Selection's field set and on the fields the
//   real ProviderRegistryReader/LaneInfo/ProviderInfo do not carry
//   (ExecutionMode, CostEstimate, HomeNode/locality, probe cache, health
//   staleness) - both sides are quoted there.
// SPORT: conductor.router/ADD (P1-E11-W3-S22-T2).

package conductor

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/provider"
)

// TaskClassRow is the six-column §5.16 task-class taxonomy row shape.
// This ticket declares only the type; the populated nine-row table is
// S-22.T4's task_classes.go to own (R-14.38) and is taken here by
// constructor injection.
type TaskClassRow struct {
	Class              string
	Reasoning          string
	CtxK               int
	Structured         bool
	SensitivityDefault provider.SensitivityTier
	LaneAffinity       string
}

// QuotaSpiller is the ordering seam FILTER 4 calls (R-40.X11): exactly
// J/S-21.T1's QuotaPolicy.NextLane method shape. Declared as an interface
// here (rather than importing the concrete *QuotaPolicy pointer directly)
// so router_test.go can supply a deterministic double without a real
// spill-order configuration.
type QuotaSpiller interface {
	NextLane(ctx context.Context, excluded []LaneID) (LaneID, error)
}

// DefaultRouter selects a provider lane via the five sequential filters.
// Build one with NewRouter; the zero value is not usable.
type DefaultRouter struct {
	registry provider.ProviderRegistryReader
	quota    QuotaSpiller
	clock    Clock
	classes  []TaskClassRow
}

// NewRouter builds a Router. No vendor names or provider identifiers are
// hardcoded anywhere in this package; registry, quota and classes are the
// only sources of lane data.
func NewRouter(registry provider.ProviderRegistryReader, quota QuotaSpiller, clock Clock, classes []TaskClassRow) *DefaultRouter {
	return &DefaultRouter{registry: registry, quota: quota, clock: clock, classes: classes}
}

// var _ Router = (*DefaultRouter)(nil) asserts *DefaultRouter satisfies
// the frozen Router seam S-22.T1's model.go declares.
var _ Router = (*DefaultRouter)(nil)

// NewDaemonRouter is the production Router-construction path
// testonly-allow.json's NewQuotaPolicy/ParseQuotaConfig/PublishDivergence
// /registry.NewReader exemptions name (all four retire_ticket
// P1-E11-W3-S22-T2): it wraps reg in J/S-20.T2's Reader, parses the
// [conductor.quota] section, builds the QuotaPolicy, publishes a
// fail-closed divergence event when the section was missing or
// unverifiable, and returns the ready DefaultRouter. The daemon
// composition root that CALLS NewDaemonRouter with a real registry, event
// bus and taxonomy table is S-22.T4's, not this ticket's - see the
// journal and this file's own new testonly-allow.json entry.
func NewDaemonRouter(reg *registry.Registry, extra map[string]interface{}, bus EventPublisher, clock Clock, classes []TaskClassRow) (*DefaultRouter, error) {
	reader := registry.NewReader(reg)
	cfg, divergent, err := ParseQuotaConfig(extra)
	if err != nil {
		return nil, err
	}
	quota := NewQuotaPolicy(cfg, clock)
	if divergent {
		reason := "conductor.router: [conductor.quota] section missing or unverifiable"
		if perr := PublishDivergence(context.Background(), bus, "conductor.router", reason); perr != nil {
			return nil, perr
		}
	}
	return NewRouter(reader, quota, clock, classes), nil
}

// laneCandidate pairs one registry lane record with its owning provider
// record - the join Select's filters read from.
type laneCandidate struct {
	lane     provider.LaneInfo
	provider provider.ProviderInfo
}

// routeSnapshot is the R-21.213 immutable metadata snapshot: the lane
// list read exactly once at Select entry. Every filter reads only this
// value; none re-reads the registry mid-pass.
type routeSnapshot struct {
	lanes []laneCandidate
}

// buildSnapshot reads the registry's lane and provider lists exactly
// once and joins them by provider name. It is called exactly once per
// Select call.
func (r *DefaultRouter) buildSnapshot(ctx context.Context) (routeSnapshot, error) {
	lanes, err := r.registry.ListLanes(ctx)
	if err != nil {
		return routeSnapshot{}, err
	}
	providers, err := r.registry.ListProviders(ctx)
	if err != nil {
		return routeSnapshot{}, err
	}
	byName := make(map[string]provider.ProviderInfo, len(providers))
	for _, p := range providers {
		byName[p.Name] = p
	}
	out := make([]laneCandidate, 0, len(lanes))
	for _, l := range lanes {
		out = append(out, laneCandidate{lane: l, provider: byName[l.ProviderName]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lane.LaneName < out[j].lane.LaneName })
	return routeSnapshot{lanes: out}, nil
}

// Select implements the frozen Router interface (S-22.T1's model.go):
// Select(ctx, req, exclude...) (provider.Selection, error). It is a thin
// wrapper over SelectExplain that discards the reason flags, because the
// frozen interface signature has no field to carry them on (see the
// journal's CONTRACT DEVIATION entry: R-40.X12 locks Selection to J/S-19
// .T1's pkg/provider type, which declares only LaneID, Provider and
// Model).
func (r *DefaultRouter) Select(ctx context.Context, req provider.ModelRequest, exclude ...string) (provider.Selection, error) {
	sel, _, err := r.SelectExplain(ctx, req, exclude...)
	return sel, err
}

// SelectExplain runs the same five-filter pipeline Select does and
// additionally returns the ReasonFlags the decision produced. It is the
// SAME function Select calls (not a second code path assembled after the
// fact, R-14.85): the explanation can never drift from the decision
// because both come from one evaluation of one routeSnapshot.
func (r *DefaultRouter) SelectExplain(ctx context.Context, req provider.ModelRequest, exclude ...string) (provider.Selection, []string, error) {
	snap, err := r.buildSnapshot(ctx)
	if err != nil {
		return provider.Selection{}, nil, err
	}
	var flags []string

	cands := excludeNamed(snap.lanes, exclude)

	cands, flags, err = filterCapability(cands, req, flags)
	if err != nil {
		return provider.Selection{}, flags, err
	}
	cands, flags, err = filterSensitivity(cands, req, flags)
	if err != nil {
		return provider.Selection{}, flags, err
	}
	cands, flags, err = filterHealth(cands, flags)
	if err != nil {
		return provider.Selection{}, flags, err
	}
	cand, flags, err := filterQuota(ctx, r.quota, snap.lanes, cands, exclude, flags)
	if err != nil {
		return provider.Selection{}, flags, err
	}
	return filterCost(cand, r.classes, req, flags)
}

// excludeNamed removes exactly the lanes named in exclude and nothing
// else (R-14.88's single capability-denied failover argument).
func excludeNamed(cands []laneCandidate, exclude []string) []laneCandidate {
	if len(exclude) == 0 {
		return cands
	}
	barred := make(map[string]bool, len(exclude))
	for _, x := range exclude {
		barred[x] = true
	}
	out := make([]laneCandidate, 0, len(cands))
	for _, c := range cands {
		if !barred[c.lane.LaneName] {
			out = append(out, c)
		}
	}
	return out
}
