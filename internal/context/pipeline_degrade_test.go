package context

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the composer's remaining degrade paths (P1-E21-W5-S46-T3): a stage
//   that RAN and rejected its own model output, and a composer counter that
//   fails while re-measuring a condensation. Both publish the stage's own
//   fixed reason with no taxonomy kind attached (nothing failed -- something
//   was declined), and both leave the assembly exactly as it was.
// Constraints: Art.1 -- doubles from pipeline_test.go and
//   pipeline_compose_test.go.
// SPORT: context-engine/pipeline-degrade-tests (ADD, P1-E21-W5-S46-T3).

// errorOnMarkerCounter counts a rune per token but fails on any text holding
// the marker, so a test can fail the composer's re-measurement of the
// condensation alone without failing the assembly that preceded it.
type errorOnMarkerCounter struct{ marker string }

func (c errorOnMarkerCounter) Count(_ context.Context, text string) (int, error) {
	if strings.Contains(text, c.marker) {
		return 0, cascade.New(cascade.KindUnavailable, "the tokenizer refused this text")
	}
	return len([]rune(text)), nil
}

// TestComposeRejectedPreStageResponseDegrades: the stage dispatched, parsed
// the response, declined it, and said why. Compose keeps the plain assembly
// and publishes the stage's own reason -- not a dispatch failure, because
// nothing failed to dispatch.
func TestComposeRejectedPreStageResponseDegrades(t *testing.T) {
	exec := &stageExecutor{replies: []string{"only-one-label"}}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPreStage(mustPreStage(t, PreStageClassify, exec))

	result, err := c.Compose(context.Background(), 100,
		[]Slot{{Label: "a", Content: "alpha"}, {Label: "b", Content: "beta"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if exec.callCount() != 1 {
		t.Errorf("executor called %d times, want exactly 1 (no retry on a rejected response)", exec.callCount())
	}
	if len(result.Slots) != 2 || result.Slots[0].ClassLabel != "" || result.Slots[1].ClassLabel != "" {
		t.Errorf("result = %+v, want the un-labelled assembly", result.Slots)
	}
	degrades := rec.degrades()
	if len(degrades) != 1 || degrades[0].Reason != stageRejectClassifyShape {
		t.Fatalf("events = %+v, want one %q", degrades, stageRejectClassifyShape)
	}
	if degrades[0].Kind != "" {
		t.Errorf("event Kind = %q, want empty: a declined response carries no failure kind", degrades[0].Kind)
	}
	if degrades[0].Stage != stagePositionPre || degrades[0].TaskClass != pipelineTaskClassClassify {
		t.Errorf("event = %+v, want the pre-assembly classify stage named", degrades[0])
	}
}

// TestComposeRejectedPostStageResponseDegrades is its twin for the post-
// assembly stage, where the stage's own counter found the answer no shorter.
func TestComposeRejectedPostStageResponseDegrades(t *testing.T) {
	assembled := "alpha beta gamma"
	exec := &stageExecutor{replies: []string{assembled}}
	c, rec := composerWithStage(t, runeCounter{})
	c.SetPostStage(mustSummarizeStage(t, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: assembled}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != assembled || result.Slots[0].Summarized {
		t.Errorf("result = %+v, want the un-condensed assembly", result.Slots)
	}
	degrades := rec.degrades()
	if len(degrades) != 1 || degrades[0].Reason != stageRejectSummarizeNotShorter {
		t.Fatalf("events = %+v, want one %q", degrades, stageRejectSummarizeNotShorter)
	}
	if degrades[0].Stage != stagePositionPost {
		t.Errorf("event Stage = %q, want %q", degrades[0].Stage, stagePositionPost)
	}
}

// TestComposeCondensationMeasurementFailureDegrades: the composer cannot
// measure the condensation, so it cannot promise the invariant about it, so it
// keeps the assembly it already measured.
func TestComposeCondensationMeasurementFailureDegrades(t *testing.T) {
	exec := &stageExecutor{replies: []string{"TOXIC"}}
	c, rec := composerWithStage(t, errorOnMarkerCounter{marker: "TOXIC"})
	c.SetPostStage(mustSummarizeStage(t, exec))

	result, err := c.Compose(context.Background(), 100, []Slot{{Label: "a", Content: "alpha beta gamma"}})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(result.Slots) != 1 || result.Slots[0].Content != "alpha beta gamma" {
		t.Errorf("result = %+v, want the measured assembly", result.Slots)
	}
	degrades := rec.degrades()
	if len(degrades) != 1 || degrades[0].Reason != stageReasonCountFailed {
		t.Fatalf("events = %+v, want one %q", degrades, stageReasonCountFailed)
	}
	if degrades[0].Kind != cascade.KindUnavailable.String() {
		t.Errorf("event Kind = %q, want %q", degrades[0].Kind, cascade.KindUnavailable.String())
	}
}
