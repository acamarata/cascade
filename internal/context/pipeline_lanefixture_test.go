package context

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the fixtures pipeline_lane_test.go routes through
//   (P1-E21-W5-S46-T3): the three-lane registry, the affinity-to-lane-name
//   mapping, the taxonomy-derived cheap-tier spill order, the real-router
//   construction per task class, the real request each stage would send, and
//   routerExecutor -- the conductor-boundary double that selects before it
//   dispatches. Split from the tests only because the two together exceed
//   Art.10.3's 300-line file cap.
// Constraints: Art.2 -- registry, spiller and clock are doubles; the router,
//   its filters and the taxonomy table are real.
// SPORT: context-engine/pipeline-lane-fixture (ADD, P1-E21-W5-S46-T3).

// cheapestFreeLaneName is the third lane this file adds to the cheap/strong
// pair summarizer_cheap_lane_test.go already declares: the "cheapest/free"
// tier classify and segment are pinned to. Its name is the affinity tier's
// own name, so laneNameForAffinity can derive it rather than guess.
const cheapestFreeLaneName = "lane-cheapest-free"

// laneNameForAffinity maps a §5.16 LaneAffinity to this fixture's lane name.
// "cheapest/free" carries a slash, which is not a lane-name character in any
// registry, so it is the one tier whose name needs mapping rather than
// concatenation.
func laneNameForAffinity(affinity string) string {
	return "lane-" + strings.ReplaceAll(affinity, "/", "-")
}

// threeLaneRegistry is twoLaneRegistry plus the cheapest/free lane, so the
// two pre-assembly classes and the one post-assembly class can be told apart
// by the lane they land on instead of sharing one "cheap" bucket.
func newThreeLaneRegistry() *twoLaneRegistry {
	r := newTwoLaneRegistry()
	r.providers = append(r.providers, provider.ProviderInfo{
		Name: "prov-cheapest-free", BaseURL: "http://127.0.0.1:8083",
		KnownModels: []string{"model-cheapest-free"}, HealthStatus: "healthy",
	})
	r.lanes = append(r.lanes, provider.LaneInfo{
		LaneName: cheapestFreeLaneName, ProviderName: "prov-cheapest-free", State: "active",
	})
	return r
}

// evictLanes marks the named lanes' providers unhealthy, which is what
// FILTER 3 removes a lane for.
func evictLanes(r *twoLaneRegistry, laneNames ...string) *twoLaneRegistry {
	evicted := make(map[string]bool, len(laneNames))
	for _, name := range laneNames {
		for _, l := range r.lanes {
			if l.LaneName == name {
				evicted[l.ProviderName] = true
			}
		}
	}
	for i := range r.providers {
		if evicted[r.providers[i].Name] {
			r.providers[i].HealthStatus = "unhealthy"
		}
	}
	return r
}

// cheapTierLaneNames derives, from the taxonomy itself, the lanes a
// cheap-pinned stage may spill across: the lane for every distinct cheap-end
// LaneAffinity in the table, in table order (which runs cheapest-first). It
// is the operator-side expression of the pin -- a cheap-pinned stage never
// spills onto the strong lane, which is the whole reason for declaring a
// cheap class at all.
func cheapTierLaneNames(rows []conductor.TaskClassRow) []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range rows {
		if !strings.Contains(row.LaneAffinity, "cheap") || seen[row.LaneAffinity] {
			continue
		}
		seen[row.LaneAffinity] = true
		out = append(out, laneNameForAffinity(row.LaneAffinity))
	}
	return out
}

// routerForClass builds a real router over reg whose spill order starts at
// the lane class's own §5.16 LaneAffinity names and then continues across the
// remaining cheap-tier lanes. It returns the router and the derived first
// lane.
func routerForClass(t *testing.T, rows []conductor.TaskClassRow, class string, reg *twoLaneRegistry) (*conductor.DefaultRouter, string) {
	t.Helper()
	affinity := laneAffinityFor(rows, class)
	if affinity == "" {
		t.Fatalf("no §5.16 row carries task class %q", class)
	}
	first := laneNameForAffinity(affinity)
	order := []conductor.LaneID{conductor.LaneID(first)}
	for _, name := range cheapTierLaneNames(rows) {
		if name != first {
			order = append(order, conductor.LaneID(name))
		}
	}
	spiller := &affinityOrderSpiller{order: order}
	return conductor.NewRouter(reg, spiller, testkit.NewFrozenClock(time.Unix(0, 0)), rows), first
}

// stageRequestFor builds the request the real stage for class would dispatch:
// its own prompt, its own §5.16 Requirements row, nothing synthesized here.
func stageRequestFor(t *testing.T, class string) provider.ModelRequest {
	t.Helper()
	exec := &stageExecutor{}
	in := &StageInput{}
	var stage PipelineStage
	switch class {
	case pipelineTaskClassClassify:
		stage = mustPreStage(t, PreStageClassify, exec)
		in.Slots = []Slot{{Label: "a", Content: "the content to route"}}
	case pipelineTaskClassSegment:
		stage = mustPreStage(t, PreStageSegment, exec)
		in.Slots = []Slot{{Label: "a", Content: "the content to route"}}
	case pipelineTaskClassSummarize:
		stage = mustSummarizeStage(t, exec)
		in.Text = "the assembled content to route"
	default:
		t.Fatalf("no stage declares task class %q", class)
	}
	impl, ok := stage.(*modelPipelineStage)
	if !ok {
		t.Fatalf("stage for %q is not a *modelPipelineStage", class)
	}
	prompt, err := impl.render(in)
	if err != nil {
		t.Fatalf("render(%s): %v", class, err)
	}
	req, err := impl.request(prompt)
	if err != nil {
		t.Fatalf("request(%s): %v", class, err)
	}
	return req
}

// mainTaskRequest builds a caller's own "code"-class request from the
// taxonomy's own row, for the independence check: the main task is the thing
// the cheap-lane pin exists to protect.
func mainTaskRequest(t *testing.T, rows []conductor.TaskClassRow) provider.ModelRequest {
	t.Helper()
	row, ok := taskClassRow(rows, string(conductor.TaskClassCode))
	if !ok {
		t.Fatal("no §5.16 row for the code task class")
	}
	return provider.ModelRequest{
		TaskID:       "main-task",
		TaskClass:    row.Class,
		Inputs:       []provider.ChatMessage{{Role: "user", Content: "the caller's own work"}},
		Requirements: provider.Requirements{Reasoning: row.Reasoning, Context: row.CtxK * 1000, Structured: row.Structured},
	}
}

// routerExecutor is the conductor-boundary double: it does what the real
// Executor does around a Select -- refuse when no lane survives, dispatch to
// the one that did -- and records both counts separately, so "the router
// refused" can be told apart from "a provider was called".
type routerExecutor struct {
	router *conductor.DefaultRouter
	output string

	mu         sync.Mutex
	selects    int
	dispatches int
	lanes      []string
}

func (e *routerExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	e.mu.Lock()
	e.selects++
	e.mu.Unlock()
	sel, err := e.router.Select(ctx, req)
	if err != nil {
		return provider.ModelResponse{}, err
	}
	e.mu.Lock()
	e.dispatches++
	e.lanes = append(e.lanes, sel.LaneID)
	e.mu.Unlock()
	return provider.ModelResponse{Output: e.output}, nil
}

func (e *routerExecutor) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.selects, e.dispatches
}
