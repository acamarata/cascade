package context

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the lane-policy proof (P1-E21-W5-S46-T3): each stage's OWN built
//   request, routed through a REAL internal/conductor.DefaultRouter over the
//   REAL §5.16 taxonomy and a three-lane fixture registry, lands on the lane
//   its task class's affinity names -- and TestComposerLanePolicy, the root
//   test the ticket's execution_guidance names, proving denial, cheap-lane
//   eviction and main-task independence at the composer boundary.
// Constraints: Art.2 -- not an integration test against a live provider. The
//   registry, the spiller and the clock are doubles; the ROUTER, its filters
//   and the taxonomy table are real.
//
//   WHAT THIS CAN AND CANNOT PROVE, inherited from S-46.T2's own cheap-lane
//   test and its CR: filters_dispatch.go records a CONTRACT DEVIATION -- the
//   real pkg/provider types carry no cost estimate, so the router has no
//   comparator that ranks lanes by price, and what decides between two
//   surviving lanes is the operator's configured spill order. So the spill
//   order here is DERIVED from the taxonomy's own LaneAffinity for the
//   request's task class (which is exactly what an operator reads it for),
//   while the expected LANE per class is written out as a literal below. A
//   stage whose class drifted, or a taxonomy row that changed tier, moves the
//   derived order off the literal expectation and turns these red.
// SPORT: context-engine/pipeline-lane-tests (ADD, P1-E21-W5-S46-T3).

// TestPipelineStageCheapLaneRealRouter is the per-class expectation table:
// each stage's own request, the derived spill order, and the literal lane it
// must land on. Change a stage's class to "code" and its case goes red, both
// on the affinity grounding check and on the selected lane.
func TestPipelineStageCheapLaneRealRouter(t *testing.T) {
	rows := conductor.TaskClasses()
	cases := []struct {
		class    string
		wantLane string
	}{
		{pipelineTaskClassClassify, cheapestFreeLaneName},
		{pipelineTaskClassSegment, cheapestFreeLaneName},
		{pipelineTaskClassSummarize, cheapLaneName},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			if affinity := laneAffinityFor(rows, tc.class); !strings.Contains(affinity, "cheap") {
				t.Fatalf("the %s row's LaneAffinity = %q, want a cheap-end tier (06-FORGE-SPEC.md §5.16)",
					tc.class, affinity)
			}
			router, _ := routerForClass(t, rows, tc.class, newThreeLaneRegistry())
			sel, flags, err := router.SelectExplain(context.Background(), stageRequestFor(t, tc.class))
			if err != nil {
				t.Fatalf("the real router refused the %s stage's own request: %v (flags %v)", tc.class, err, flags)
			}
			if sel.LaneID != tc.wantLane {
				t.Errorf("selected lane = %q, want %q", sel.LaneID, tc.wantLane)
			}
			if sel.LaneID == strongLaneName {
				t.Errorf("a %s stage reached the strong lane, which the §5.16 pin exists to prevent", tc.class)
			}
		})
	}
}

// TestComposerLanePolicy is the root test the ticket's execution_guidance
// names. Its three subtests are the three claims that matter at the composer
// boundary: a refused selection degrades, an evicted cheap lane degrades
// without borrowing the strong lane, and none of it touches the caller's own
// task.
func TestComposerLanePolicy(t *testing.T) {
	rows := conductor.TaskClasses()
	t.Run("denial", func(t *testing.T) { laneDenialCase(t, rows) })
	t.Run("fallback", func(t *testing.T) { laneFallbackCase(t, rows) })
	t.Run("main task independence", func(t *testing.T) { laneIndependenceCase(t, rows) })
}

// laneDenialCase: every lane evicted, so the real router refuses the stage's
// request. Compose succeeds un-staged, no provider is called, and the refusal
// is published once -- there is no second attempt on any lane.
func laneDenialCase(t *testing.T, rows []conductor.TaskClassRow) {
	t.Helper()
	reg := evictLanes(newThreeLaneRegistry(), cheapestFreeLaneName, cheapLaneName, strongLaneName)
	router, _ := routerForClass(t, rows, pipelineTaskClassClassify, reg)
	exec := &routerExecutor{router: router, output: "instruction"}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta"}})
	if err != nil {
		t.Fatalf("Compose failed because the router had no lane: %v", err)
	}
	selects, dispatches := exec.counts()
	if selects != 1 || dispatches != 0 {
		t.Errorf("selects/dispatches = %d/%d, want 1/0 (one refusal, no provider call, no retry)",
			selects, dispatches)
	}
	if result.Slots[0].ClassLabel != "" {
		t.Errorf("result was labelled %q despite the refusal", result.Slots[0].ClassLabel)
	}
	if degrades := rec.degrades(); len(degrades) != 1 || degrades[0].Reason != stageReasonDispatchFailed {
		t.Errorf("events = %+v, want one dispatch-failed degrade", degrades)
	}
}

// laneFallbackCase: both cheap-tier lanes evicted while the strong lane stays
// healthy. The stage degrades rather than borrowing the strong lane, the main
// assembly is untouched, and the caller's own task still routes.
func laneFallbackCase(t *testing.T, rows []conductor.TaskClassRow) {
	t.Helper()
	reg := evictLanes(newThreeLaneRegistry(), cheapestFreeLaneName, cheapLaneName)
	router, _ := routerForClass(t, rows, pipelineTaskClassClassify, reg)
	exec := &routerExecutor{router: router, output: "instruction"}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta"}})
	if err != nil {
		t.Fatalf("Compose failed because both cheap lanes were evicted: %v", err)
	}
	if _, dispatches := exec.counts(); dispatches != 0 {
		t.Errorf("dispatches = %d, want 0: an evicted cheap lane must degrade, never borrow the strong lane",
			dispatches)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != "alpha beta" || result.Slots[0].ClassLabel != "" {
		t.Errorf("result = %+v, want the main assembly untouched", result.Slots)
	}
	if degrades := rec.degrades(); len(degrades) != 1 {
		t.Errorf("events = %+v, want exactly one degrade event", degrades)
	}
	requireMainTaskOnStrongLane(t, rows, reg)
}

// laneIndependenceCase: on a healthy registry the post-assembly stage lands on
// the cheap lane while the caller's own code-class task lands on the strong
// one, which is the whole point of the pin.
func laneIndependenceCase(t *testing.T, rows []conductor.TaskClassRow) {
	t.Helper()
	reg := newThreeLaneRegistry()
	stageRouter, _ := routerForClass(t, rows, pipelineTaskClassSummarize, reg)
	exec := &routerExecutor{router: stageRouter, output: "alpha"}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPostStage(mustSummarizeStage(t, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta gamma"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if result.Slots[0].Content != "alpha" {
		t.Fatalf("result = %+v, want the condensation applied on the healthy path", result.Slots)
	}
	if degrades := rec.degrades(); len(degrades) != 0 {
		t.Fatalf("events = %+v, want none on the healthy path", degrades)
	}
	if exec.lanes[0] != cheapLaneName {
		t.Errorf("the post-assembly stage routed to %q, want the cheap lane %q", exec.lanes[0], cheapLaneName)
	}
	if lane := requireMainTaskOnStrongLane(t, rows, reg); lane == exec.lanes[0] {
		t.Error("the stage and the main task landed on the same lane; the pin bought nothing")
	}
}

// requireMainTaskOnStrongLane routes a real code-class request over the same
// registry and taxonomy and asserts it reaches the strong lane, returning the
// lane it selected.
func requireMainTaskOnStrongLane(t *testing.T, rows []conductor.TaskClassRow, reg *twoLaneRegistry) string {
	t.Helper()
	router, _ := routerForClass(t, rows, string(conductor.TaskClassCode), reg)
	sel, err := router.Select(context.Background(), mainTaskRequest(t, rows))
	if err != nil {
		t.Fatalf("the caller's own task could not be routed: %v", err)
	}
	if sel.LaneID != strongLaneName {
		t.Errorf("the main task selected %q, want the strong lane %q", sel.LaneID, strongLaneName)
	}
	return sel.LaneID
}

// TestComposerLanePolicyRequestsCarryNoWideningPolicy pins the privacy half
// of the pin: no stage request may name a Policy, widen Sensitivity, or allow
// external execution, since FILTER 0 applies the thread's own privacy mode
// and this caller's only duty is not to loosen it.
func TestComposerLanePolicyRequestsCarryNoWideningPolicy(t *testing.T) {
	for _, class := range []string{pipelineTaskClassClassify, pipelineTaskClassSegment, pipelineTaskClassSummarize} {
		req := stageRequestFor(t, class)
		if req.Policy != (provider.Policy{}) {
			t.Errorf("%s request carries Policy %+v, want the zero value", class, req.Policy)
		}
		if req.Sensitivity != provider.SensitivityTier(0) {
			t.Errorf("%s request declares Sensitivity %v, want it unset so restricted is inherited",
				class, req.Sensitivity)
		}
		if req.TaskClass != class {
			t.Errorf("request TaskClass = %q, want %q", req.TaskClass, class)
		}
		if req.RequiredCapabilities != (provider.RequiredCapabilities{}) {
			t.Errorf("%s request requires capabilities %+v; a cheap lane need advertise none",
				class, req.RequiredCapabilities)
		}
	}
}
