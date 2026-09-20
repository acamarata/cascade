package context

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: TestSummarizerCheapLane, the usage test this ticket's
//   acceptance_criteria requires: the ModelRequest the Summarizer actually
//   builds, routed through a REAL internal/conductor.DefaultRouter over the
//   REAL §5.16 taxonomy and a two-lane registry, lands on the cheap lane and
//   carries the cost stage's own ReasonFlag.
// Constraints: Art.2 -- not an integration test against a live provider. The
//   registry, the spiller and the clock are doubles; the ROUTER, its five
//   filters and the taxonomy table are the real ones.
//
//   WHAT THIS CAN AND CANNOT PROVE. filters_dispatch.go records a CONTRACT
//   DEVIATION: the real pkg/provider types carry no cost estimate, so the
//   router has no comparator that could rank two lanes by price. What decides
//   between two surviving lanes is the operator's configured spill order, so
//   this test derives that order from the taxonomy's own LaneAffinity for the
//   request's task class -- which is exactly what an operator reads it for --
//   and then asserts the selected lane is the CHEAP one. A task class that
//   drifted (to "code", say, whose affinity is "strong") would move the
//   derived order onto the expensive lane and turn this test red, which is
//   the drift it is here to catch.
// SPORT: context-engine/summarizer-cheap-lane (ADD, P1-E21-W5-S46-T2).

// The two lanes' names encode the taxonomy affinity each one serves, so the
// spill order can be derived from a §5.16 row rather than typed as a literal.
const (
	cheapLaneName  = "lane-cheap"
	strongLaneName = "lane-strong"
)

// twoLaneRegistry is a provider.ProviderRegistryReader double with exactly
// two healthy, controller-local lanes: one cheap, one expensive. Its own
// double rather than internal/conductor's package-internal fakeRegistry,
// which this package cannot see.
type twoLaneRegistry struct {
	providers []provider.ProviderInfo
	lanes     []provider.LaneInfo
}

func newTwoLaneRegistry() *twoLaneRegistry {
	return &twoLaneRegistry{
		providers: []provider.ProviderInfo{
			{Name: "prov-cheap", BaseURL: "http://127.0.0.1:8081", KnownModels: []string{"model-cheap"}, HealthStatus: "healthy"},
			{Name: "prov-strong", BaseURL: "http://127.0.0.1:8082", KnownModels: []string{"model-strong"}, HealthStatus: "healthy"},
		},
		lanes: []provider.LaneInfo{
			{LaneName: cheapLaneName, ProviderName: "prov-cheap", State: "active"},
			{LaneName: strongLaneName, ProviderName: "prov-strong", State: "active"},
		},
	}
}

func (r *twoLaneRegistry) GetProvider(_ context.Context, name string) (provider.ProviderInfo, error) {
	for _, p := range r.providers {
		if p.Name == name {
			return p, nil
		}
	}
	return provider.ProviderInfo{}, cascade.New(cascade.KindNotFound, "twoLaneRegistry: unknown provider")
}

func (r *twoLaneRegistry) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	return r.providers, nil
}

func (r *twoLaneRegistry) ListLanes(_ context.Context) ([]provider.LaneInfo, error) {
	return r.lanes, nil
}

func (r *twoLaneRegistry) ListPool(_ context.Context, pool string) ([]provider.LaneInfo, error) {
	var out []provider.LaneInfo
	for _, l := range r.lanes {
		if l.PoolMembership == pool {
			out = append(out, l)
		}
	}
	return out, nil
}

func (r *twoLaneRegistry) GetByModel(_ context.Context, model string) ([]provider.ProviderInfo, error) {
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

var _ provider.ProviderRegistryReader = (*twoLaneRegistry)(nil)

// affinityOrderSpiller is a conductor.QuotaSpiller double that offers lanes
// in one configured order: the order an operator derives from the task
// class's LaneAffinity.
type affinityOrderSpiller struct{ order []conductor.LaneID }

func (s *affinityOrderSpiller) NextLane(_ context.Context, excluded []conductor.LaneID) (conductor.LaneID, error) {
	barred := make(map[conductor.LaneID]bool, len(excluded))
	for _, x := range excluded {
		barred[x] = true
	}
	for _, lane := range s.order {
		if !barred[lane] {
			return lane, nil
		}
	}
	return "", conductor.ErrAllLanesExhausted
}

// laneAffinityFor looks up class's LaneAffinity in the real taxonomy rows,
// mirroring internal/conductor/filters_dispatch.go's own resolveLaneAffinity
// lookup shape (unexported there, so re-implemented here rather than
// reaching into conductor's internals).
func laneAffinityFor(rows []conductor.TaskClassRow, class string) string {
	for _, row := range rows {
		if row.Class == class {
			return row.LaneAffinity
		}
	}
	return ""
}

// TestSummarizerCheapLane routes the real request through the real router.
func TestSummarizerCheapLane(t *testing.T) {
	rows := conductor.TaskClasses()
	if got := laneAffinityFor(rows, string(conductor.TaskClassSummarize)); got != "cheap" {
		// Grounding check, not a tautology: it confirms independently that
		// the row this test's expectation rests on still says what the
		// contract text asserts, rather than assuming it.
		t.Fatalf("the summarize row's LaneAffinity = %q, want %q (06-FORGE-SPEC.md §5.16)", got, "cheap")
	}

	s := newTestSummarizer(t, &fakeExecutor{output: "summary"}, storetest.NewMemStore())
	req, err := s.buildModelRequest(context.Background(), "thread-a", GranularityThread, "", "the content to summarize")
	if err != nil {
		t.Fatalf("buildModelRequest: %v", err)
	}

	affinity := laneAffinityFor(rows, req.TaskClass)
	spiller := &affinityOrderSpiller{order: []conductor.LaneID{conductor.LaneID("lane-" + affinity)}}
	router := conductor.NewRouter(newTwoLaneRegistry(), spiller, testkit.NewFrozenClock(time.Unix(0, 0)), rows)

	sel, flags, err := router.SelectExplain(context.Background(), req)
	if err != nil {
		t.Fatalf("the real router refused the summarizer's own request: %v (flags %v)", err, flags)
	}
	if sel.LaneID != cheapLaneName {
		t.Errorf("selected lane = %q, want the cheap lane %q (affinity resolved from the request's task class was %q)",
			sel.LaneID, cheapLaneName, affinity)
	}
	// This flag is appended on EVERY successful Select (the cost stage has
	// no comparator yet, see the header note), so its presence proves the
	// selection reached the cost stage and nothing more. The lane
	// assertion above is the one that carries the cheap-lane claim.
	if !containsFlag(sel.ReasonFlags, "cost:cheapest-selected") {
		t.Errorf("Selection.ReasonFlags = %v, want the cost stage's own flag", sel.ReasonFlags)
	}
	if !anyFlagContains(sel.ReasonFlags, "quota:") || !anyFlagContains(sel.ReasonFlags, cheapLaneName) {
		t.Errorf("Selection.ReasonFlags = %v, want the lane-selection trace to name the chosen lane", sel.ReasonFlags)
	}
}

// TestSummarizerCheapLaneEveryGranularityRoutesTheSameWay asserts the lane
// choice is a property of the task class, not of the granularity: all three
// levels must reach the cheap lane, so no level quietly escalates.
func TestSummarizerCheapLaneEveryGranularityRoutesTheSameWay(t *testing.T) {
	rows := conductor.TaskClasses()
	s := newTestSummarizer(t, &fakeExecutor{output: "summary"}, storetest.NewMemStore())
	router := conductor.NewRouter(newTwoLaneRegistry(),
		&affinityOrderSpiller{order: []conductor.LaneID{conductor.LaneID("lane-" + laneAffinityFor(rows, string(conductor.TaskClassSummarize)))}},
		testkit.NewFrozenClock(time.Unix(0, 0)), rows)

	for _, level := range []Granularity{GranularityTurnWindow, GranularityThread, GranularityEpoch} {
		req, err := s.buildModelRequest(context.Background(), "e", level, "", "content")
		if err != nil {
			t.Fatalf("buildModelRequest(%v): %v", level, err)
		}
		sel, err := router.Select(context.Background(), req)
		if err != nil {
			t.Fatalf("Select(%v): %v", level, err)
		}
		if sel.LaneID != cheapLaneName {
			t.Errorf("level %v routed to %q, want the cheap lane %q", level, sel.LaneID, cheapLaneName)
		}
	}
}

// containsFlag reports whether flags contains target exactly.
func containsFlag(flags []string, target string) bool {
	for _, f := range flags {
		if f == target {
			return true
		}
	}
	return false
}

// anyFlagContains reports whether any flag contains substr.
func anyFlagContains(flags []string, substr string) bool {
	for _, f := range flags {
		if strings.Contains(f, substr) {
			return true
		}
	}
	return false
}

// TestSummarizerCheapLaneRequestNeverRequiresCapabilities asserts the
// request the Summarizer builds requires no RequiredCapabilities dimension
// -- a cheap/free lane need not advertise vision, tool-use, search, etc.,
// and this ticket must never accidentally exclude one by over-declaring.
func TestSummarizerCheapLaneRequestNeverRequiresCapabilities(t *testing.T) {
	s := newTestSummarizer(t, &fakeExecutor{output: "summary"}, storetest.NewMemStore())
	req, err := s.buildModelRequest(context.Background(), "e", GranularityThread, "", "c")
	if err != nil {
		t.Fatalf("buildModelRequest: %v", err)
	}
	rc := req.RequiredCapabilities
	if rc.Search || rc.URLFetch || rc.Vision || rc.ToolUse || rc.LongContext || rc.StructuredOutput {
		t.Errorf("RequiredCapabilities = %+v, want the zero value (no dimension required)", rc)
	}
}

// TestSummarizerDispatchesTheContentItWasGiven is a scoping check: the
// request the engine dispatches carries the source content it was asked to
// summarize, so the tests above are routing a request with a real payload.
func TestSummarizerDispatchesTheContentItWasGiven(t *testing.T) {
	exec := &fakeExecutor{output: "summary"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	if _, err := s.GetSummary(context.Background(), "e", GranularityThread, "UNIQUE-MARKER-42", "v1"); err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if !strings.Contains(exec.requestAt(0).Inputs[0].Content, "UNIQUE-MARKER-42") {
		t.Error("dispatched request does not contain the source content it was asked to summarize")
	}
}
