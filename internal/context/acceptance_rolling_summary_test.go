// Package context (acceptance_rolling_summary_test.go): Purpose: the
// P1-E21-W5-S46-T5 Story 2 acceptance proof for the rolling-summary
// pipeline (S-46.T2) and the cheap-lane pin (S-46.T3), both driven through
// their real units with a fake provider.ModelExecutor at the true
// conductor-boundary seam - reusing pipeline_lanefixture_test.go's
// existing real-router fixtures (newThreeLaneRegistry, routerForClass,
// routerExecutor) rather than re-inventing them.
// Inputs: a fixed short model output (the rolling summarizer's regen
// dispatch), and each pipeline stage's own real request routed through a
// real internal/conductor.DefaultRouter over the real §5.16 taxonomy.
// Outputs: none (t.Fatal only).
// Constraints: no network; production code under test never imports
// internal/conductor (summarizer_dispatch.go/pipeline.go's own stance) -
// this test file may, since it is test-only.
// SPORT: internal/context acceptance (ADD) (P1-E21-W5-S46-T5).
package context

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestAcceptanceRollingSummaryGranularities is the checks-list entry
// point for the "S-46.T2 rolling-summary pipeline is exercised across all
// 3 granularities" acceptance line: one subtest per R-14.61 granularity,
// selected the same way Compose selects one (SelectGranularity over the
// remaining budget), each asserting a non-empty summary within that
// level's own MaxSummaryTokens ceiling - the real checkOutputSize gate
// (summarizer_dispatch.go), not a re-derived bound.
func TestAcceptanceRollingSummaryGranularities(t *testing.T) {
	store := storetest.NewMemStore()
	cases := []struct {
		name      string
		budget    int
		wantLevel Granularity
	}{
		{"turn-window", 600, GranularityTurnWindow},
		{"thread", 300, GranularityThread},
		{"epoch", 50, GranularityEpoch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectGranularity(tc.budget); got != tc.wantLevel {
				t.Fatalf("SelectGranularity(%d) = %v, want %v (R-14.61 budget thresholds)",
					tc.budget, got, tc.wantLevel)
			}
			exec := &fakeExecutor{output: "a rolling summary covering the material observed so far"}
			s := newTestSummarizer(t, exec, store)
			outcome, err := s.GetSummary(context.Background(), "acceptance-entity-"+tc.name, tc.wantLevel,
				"a long window of conversation content to summarize at this granularity", "v1")
			if err != nil {
				t.Fatalf("GetSummary(%s): %v", tc.name, err)
			}
			if outcome.Record.Content == "" {
				t.Fatalf("GetSummary(%s) returned an empty summary, want non-empty", tc.name)
			}
			bound := MaxSummaryTokens(tc.wantLevel)
			n, err := provider.NaiveTokenCounter{}.Count(context.Background(), outcome.Record.Content)
			if err != nil {
				t.Fatalf("counting the summary: %v", err)
			}
			if n > bound {
				t.Fatalf("GetSummary(%s) summary measures %d tokens, over its %d-token ceiling", tc.name, n, bound)
			}
		})
	}
}

// TestAcceptancePipelineCheapLaneOnly is the checks-list entry point for
// "the S-46.T3 pre/post pipelines are verified to route exclusively to
// cheap lanes (usage count asserted; no strong-lane dispatch observed)":
// both the classify pre-stage and the summarize post-stage are attached to
// one real Composer and driven through one real Compose call, each over
// its OWN real internal/conductor.DefaultRouter/taxonomy/three-lane
// fixture (pipeline_lanefixture_test.go); every recorded dispatch's lane
// is asserted to be a cheap-tier lane and never the strong lane.
func TestAcceptancePipelineCheapLaneOnly(t *testing.T) {
	rows := conductor.TaskClasses()
	cheapTier := map[string]bool{}
	for _, name := range cheapTierLaneNames(rows) {
		cheapTier[name] = true
	}
	if cheapTier[strongLaneName] {
		t.Fatalf("the strong lane %q is in the derived cheap tier, which invalidates this test's own "+
			"assertion boundary", strongLaneName)
	}

	classifyRouter, _ := routerForClass(t, rows, pipelineTaskClassClassify, newThreeLaneRegistry())
	classifyExec := &routerExecutor{router: classifyRouter, output: "instruction"}
	summarizeRouter, _ := routerForClass(t, rows, pipelineTaskClassSummarize, newThreeLaneRegistry())
	summarizeExec := &routerExecutor{router: summarizeRouter, output: "condensed"}

	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, classifyExec))
	c.SetPostStage(mustSummarizeStage(t, summarizeExec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta gamma"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if degrades := rec.degrades(); len(degrades) != 0 {
		t.Fatalf("degrade events = %+v, want none on this healthy fixture", degrades)
	}
	if len(result.Slots) != 1 {
		t.Fatalf("result.Slots = %+v, want exactly one composed slot", result.Slots)
	}

	_, classifyDispatches := classifyExec.counts()
	_, summarizeDispatches := summarizeExec.counts()
	if classifyDispatches == 0 || summarizeDispatches == 0 {
		t.Fatalf("dispatch usage counts = classify %d, summarize %d, want both > 0 (both stages must "+
			"actually run for this test to prove anything)", classifyDispatches, summarizeDispatches)
	}
	for _, lane := range append(append([]string{}, classifyExec.lanes...), summarizeExec.lanes...) {
		if lane == strongLaneName {
			t.Fatalf("a pipeline dispatch reached the strong lane %q; the §5.16 cheap-lane pin exists "+
				"to prevent exactly this", strongLaneName)
		}
		if !cheapTier[lane] {
			t.Fatalf("dispatch landed on lane %q, which is neither a derived cheap-tier lane nor the "+
				"strong lane - an unexpected fixture lane", lane)
		}
	}
}
