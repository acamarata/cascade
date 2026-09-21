package context

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the composer half of the pipeline seam (P1-E21-W5-S46-T3): that
//   Compose's OWN result carries what a stage produced (labels, split slots,
//   a condensed assembly with re-measured token accounting), that a stage
//   which cannot run degrades to plain assembly with one published event
//   instead of failing the call, and that no stage means no dispatch.
// Constraints: Art.1 -- doubles from pipeline_test.go plus the event
//   recorder below, all under _test.go.
// SPORT: context-engine/pipeline-compose-tests (ADD, P1-E21-W5-S46-T3).

// stageEvents records everything Compose publishes.
type stageEvents struct {
	mu   sync.Mutex
	seen []StageEvent
}

func (r *stageEvents) Publish(_ context.Context, event StageEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, event)
}

func (r *stageEvents) degrades() []StageDegradedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]StageDegradedEvent, 0, len(r.seen))
	for _, e := range r.seen {
		if e.Degraded != nil {
			out = append(out, *e.Degraded)
		}
	}
	return out
}

// bigForMarkerCounter counts a rune per token, except that any text holding
// the marker measures enormous. It stands in for the ordinary case of two
// tokenizers disagreeing, which is why the composer re-measures rather than
// trusting the stage's own verdict.
type bigForMarkerCounter struct{ marker string }

func (c bigForMarkerCounter) Count(_ context.Context, text string) (int, error) {
	if c.marker != "" && strings.Contains(text, c.marker) {
		return 100000, nil
	}
	return len([]rune(text)), nil
}

// composerWithStage builds a Composer over counter with an event recorder
// attached and returns both.
func composerWithStage(t *testing.T, counter interface {
	Count(context.Context, string) (int, error)
}) (*Composer, *stageEvents) {
	t.Helper()
	c := mustComposer(t, counter, nil)
	rec := &stageEvents{}
	c.SetStageEvents(rec)
	return c, rec
}

// TestComposeWithoutStagesDispatchesNothing: the hooks are opt-in, and an
// explicitly nil stage is the documented "disabled" value.
func TestComposeWithoutStagesDispatchesNothing(t *testing.T) {
	exec := &stageExecutor{replies: []string{"instruction"}}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(nil)
	c.SetPostStage(nil)
	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if exec.callCount() != 0 {
		t.Errorf("executor called %d times with no stage attached, want 0", exec.callCount())
	}
	if len(rec.degrades()) != 0 {
		t.Errorf("events = %+v, want none", rec.degrades())
	}
	if len(result.Slots) != 1 || result.Slots[0].ClassLabel != "" {
		t.Errorf("result = %+v, want the plain assembly", result.Slots)
	}
}

// TestComposePreStageLabelsTheResult: Compose records the classify stage's
// labels on the ComposedResult. Drop the preStage Execute call in
// pipeline_compose.go and this goes red (labels empty).
func TestComposePreStageLabelsTheResult(t *testing.T) {
	exec := &stageExecutor{replies: []string{"instruction\nhistory"}}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))

	slots := []Slot{{Kind: SlotKindTier, Label: "a", Content: "alpha"}, {Kind: SlotKindHistory, Label: "b", Content: "beta"}}
	result, err := c.Compose(context.Background(), 100, slots)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if exec.callCount() != 1 {
		t.Fatalf("pre-stage dispatched %d times, want exactly 1", exec.callCount())
	}
	if len(result.Slots) != 2 {
		t.Fatalf("result slots = %d, want 2", len(result.Slots))
	}
	if result.Slots[0].ClassLabel != "instruction" || result.Slots[1].ClassLabel != "history" {
		t.Errorf("result labels = %q / %q, want instruction / history",
			result.Slots[0].ClassLabel, result.Slots[1].ClassLabel)
	}
	if slots[0].ClassLabel != "" {
		t.Error("the caller's own slice was mutated; Compose promises the caller's input is read-only")
	}
	if len(rec.degrades()) != 0 {
		t.Errorf("events = %+v, want none on the accepted path", rec.degrades())
	}
}

// TestComposePreStageSplitChangesTheAssembly: what the segment stage handed
// back is what the assembly loop composed -- three slots where the caller
// supplied one, with the content preserved.
func TestComposePreStageSplitChangesTheAssembly(t *testing.T) {
	long := strings.Repeat("ab", 20)
	exec := &stageExecutor{replies: []string{"1:10,20\n2:0"}}
	c, _ := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageSegment, exec))

	result, err := c.Compose(context.Background(), 100,
		[]Slot{{Label: "long", Content: long}, {Label: "short", Content: "tiny"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 4 {
		t.Fatalf("result slots = %d, want 4 (the over-share slot split into 3)", len(result.Slots))
	}
	rejoined := result.Slots[0].Content + result.Slots[1].Content + result.Slots[2].Content
	if rejoined != long {
		t.Errorf("the composed parts do not rejoin to the caller's content: %q", rejoined)
	}
	if result.TokensUsed != len([]rune(long))+4 {
		t.Errorf("TokensUsed = %d, want the parts plus the short slot measured whole", result.TokensUsed)
	}
}

// TestComposePostStageCondensesAndRemeasures: the condensation replaces the
// assembled content and Compose re-measures it, so the ComposedResult's own
// accounting still describes what it returns (T1's invariant).
func TestComposePostStageCondensesAndRemeasures(t *testing.T) {
	exec := &stageExecutor{replies: []string{"alpha delta"}}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPostStage(mustSummarizeStage(t, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{
		{Kind: SlotKindTier, Label: "a", Content: "alpha beta"},
		{Kind: SlotKindMemory, Label: "b", Content: "gamma delta"},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != "alpha delta" {
		t.Fatalf("result slots = %+v, want the single condensed slot", result.Slots)
	}
	if !result.Slots[0].Summarized || result.Slots[0].Label != postStageCondensedLabel {
		t.Errorf("condensed slot = %+v, want it marked summarized and labelled %q",
			result.Slots[0], postStageCondensedLabel)
	}
	if want := len([]rune("alpha delta")); result.TokensUsed != want || result.Slots[0].Tokens != want {
		t.Errorf("TokensUsed = %d / slot tokens = %d, want %d re-measured by the composer's own counter",
			result.TokensUsed, result.Slots[0].Tokens, want)
	}
	if result.TokensUsed > result.Budget {
		t.Errorf("TokensUsed %d exceeds Budget %d after condensation", result.TokensUsed, result.Budget)
	}
	if len(rec.degrades()) != 0 {
		t.Errorf("events = %+v, want none on the accepted path", rec.degrades())
	}
}

// TestComposePostStageCondensationRefusedByComposerCounter: the stage's own
// counter accepted the condensation; the composer's counter -- the one the
// invariant is stated in -- says it is bigger, so the assembly stands and the
// refusal is published.
func TestComposePostStageCondensationRefusedByComposerCounter(t *testing.T) {
	exec := &stageExecutor{replies: []string{"WIDE"}}
	c, rec := composerWithStage(t, bigForMarkerCounter{marker: "WIDE"})
	c.SetPostStage(mustSummarizeStage(t, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta gamma"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != "alpha beta gamma" {
		t.Fatalf("result = %+v, want the un-condensed assembly", result.Slots)
	}
	degrades := rec.degrades()
	if len(degrades) != 1 || degrades[0].Reason != stageReasonCondenseRejected {
		t.Fatalf("events = %+v, want one %q", degrades, stageReasonCondenseRejected)
	}
	if degrades[0].Stage != stagePositionPost || degrades[0].TaskClass != pipelineTaskClassSummarize {
		t.Errorf("event = %+v, want the post-assembly summarize stage named", degrades[0])
	}
}

// TestComposeDegradesWhenTheStageCannotRun is D2's core promise, for both
// positions and for both a lane-unavailable refusal and a policy denial:
// Compose SUCCEEDS with the un-staged result, publishes exactly one event
// carrying the refusal's taxonomy kind, and dispatches exactly once -- there
// is no retry, least of all onto a stronger lane.
func TestComposeDegradesWhenTheStageCannotRun(t *testing.T) {
	cases := []struct {
		name string
		kind cascade.Kind
		post bool
		want string
	}{
		{"pre-stage, no lane available", cascade.KindUnavailable, false, stagePositionPre},
		{"pre-stage, policy denial", cascade.KindPolicyDenied, false, stagePositionPre},
		{"post-stage, no lane available", cascade.KindUnavailable, true, stagePositionPost},
		{"post-stage, policy denial", cascade.KindPolicyDenied, true, stagePositionPost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &stageExecutor{err: cascade.New(tc.kind, "the router refused this request")}
			c, rec := composerWithStage(t, runeCounter{})
			if tc.post {
				c.SetPostStage(mustSummarizeStage(t, exec))
			} else {
				c.SetPreStage(mustPreStage(t, PreStageClassify, exec))
			}
			result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta"}})
			if err != nil {
				t.Fatalf("Compose returned an error because a cheap lane was unavailable: %v", err)
			}
			if len(result.Slots) != 1 || result.Slots[0].Content != "alpha beta" ||
				result.Slots[0].ClassLabel != "" || result.Slots[0].Summarized {
				t.Errorf("result = %+v, want the plain un-staged assembly", result.Slots)
			}
			if got := exec.callCount(); got != 1 {
				t.Errorf("executor called %d times, want exactly 1 (no retry on another lane)", got)
			}
			degrades := rec.degrades()
			if len(degrades) != 1 {
				t.Fatalf("events = %+v, want exactly one degrade event", degrades)
			}
			if degrades[0].Stage != tc.want || degrades[0].Reason != stageReasonDispatchFailed {
				t.Errorf("event = %+v, want stage %q reason %q", degrades[0], tc.want, stageReasonDispatchFailed)
			}
			if degrades[0].Kind != tc.kind.String() {
				t.Errorf("event Kind = %q, want %q", degrades[0].Kind, tc.kind.String())
			}
		})
	}
}

// TestComposeDegradesWithNoPublisherAttached: a nil publisher drops the event
// and changes nothing else, matching NewSummarizer's own events contract.
func TestComposeDegradesWithNoPublisherAttached(t *testing.T) {
	exec := &stageExecutor{err: cascade.New(cascade.KindUnavailable, "no lane")}
	c := mustComposer(t, runeCounter{}, nil)
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))
	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != "alpha" {
		t.Errorf("result = %+v, want the plain assembly", result.Slots)
	}
}

// TestComposeRunsNoStageOverZeroSlots: the zero-slots early return happens
// before either hook, so an empty compose never spends a model call.
func TestComposeRunsNoStageOverZeroSlots(t *testing.T) {
	exec := &stageExecutor{replies: []string{"instruction"}}
	c, _ := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))
	c.SetPostStage(mustSummarizeStage(t, exec))
	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a"}, {Label: "b"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if result.ZeroSlots == nil {
		t.Error("ZeroSlots event missing")
	}
	if exec.callCount() != 0 {
		t.Errorf("executor called %d times over zero content, want 0", exec.callCount())
	}
}
