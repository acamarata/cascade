package context

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the composer side of the pipeline seam (P1-E21-W5-S46-T3): the
//   two methods Compose (composer_core.go) calls to run an attached
//   PipelineStage, the re-measurement that folds a post-stage's
//   condensation back into a ComposedResult without breaking T1's
//   bounded-context invariant, and the degrade publishing that makes a
//   stage which could not run VISIBLE instead of silent.
// Inputs: the Composer's own preStage/postStage/events seams, plus the
//   slots or result being composed.
// Outputs: staged slots, a possibly-condensed ComposedResult, and
//   StageDegradedEvents.
// Constraints: DEGRADE, NEVER ABORT. Every failure path here returns the
//   un-staged value and publishes one event; none of them returns an
//   error, because Compose's caller asked for context and a busy or
//   refusing cheap lane is not a reason to hand it a failure instead. It
//   is also why no retry on a stronger lane exists anywhere in this file:
//   the whole point of pinning these stages to cheap lanes (§5.16) is that
//   they never compete for expensive-lane capacity, and a fallback to a
//   strong lane would spend exactly the capacity the pin protects.
//   This file lives beside pipeline.go rather than inside composer_core.go
//   because both would otherwise exceed Art.10.3's 300-line file cap; see
//   this ticket's report for the declared scope excursion.
// SPORT: context-engine/pipeline-compose (ADD, P1-E21-W5-S46-T3).

// The fixed degrade reasons this file publishes. A stage's own rejection
// reasons are declared in pipeline_transform.go; these two cover the stage
// failing outright and the composer refusing a condensation it could not
// fit.
const (
	stageReasonDispatchFailed   = "the stage could not run; composition continued un-staged"
	stageReasonCondenseRejected = "the condensed text did not measure smaller than the assembly it would replace"
	stageReasonCountFailed      = "the condensed text could not be measured; the assembly stands"
)

// The two stage positions, as StageDegradedEvent.Stage reports them.
const (
	stagePositionPre  = "pre-assembly"
	stagePositionPost = "post-assembly"
)

// postStageCondensedLabel is the content-free Label the single condensed
// slot carries once a post-assembly stage's condensation is accepted. The
// original slots' own Labels cannot survive: one condensed text replaces
// all of them, so claiming any one of their labels would misreport which
// slot the content came from.
const postStageCondensedLabel = "condensed"

// runPreStage runs the attached pre-assembly stage over a copy of slots and
// returns whichever slot list assembly should use: the staged one when the
// stage applied, the caller's own when it did not.
func (c *Composer) runPreStage(ctx context.Context, slots []Slot) []Slot {
	if c.preStage == nil {
		return slots
	}
	in := &StageInput{Slots: cloneSlots(slots)}
	if err := c.preStage.Execute(ctx, in); err != nil {
		c.publishDegrade(ctx, stagePositionPre, c.preStage.TaskClass(), stageReasonDispatchFailed, err)
		return slots
	}
	if in.Degraded != "" {
		c.publishDegrade(ctx, stagePositionPre, c.preStage.TaskClass(), in.Degraded, nil)
		return slots
	}
	return in.Slots
}

// runPostStage runs the attached post-assembly stage over the assembled
// text and returns either the condensed result or result unchanged.
func (c *Composer) runPostStage(ctx context.Context, result ComposedResult) ComposedResult {
	if c.postStage == nil {
		return result
	}
	assembled := pipelineInputFromComposed(result.Slots)
	if assembled == "" {
		return result
	}
	class := c.postStage.TaskClass()
	in := &StageInput{Text: assembled}
	if err := c.postStage.Execute(ctx, in); err != nil {
		c.publishDegrade(ctx, stagePositionPost, class, stageReasonDispatchFailed, err)
		return result
	}
	if in.Degraded != "" {
		c.publishDegrade(ctx, stagePositionPost, class, in.Degraded, nil)
		return result
	}
	if in.Text == assembled {
		return result
	}
	return c.applyCondensed(ctx, result, class, in.Text)
}

// applyCondensed re-measures condensed with the Composer's OWN TokenCounter
// -- the one T1's invariant is stated in terms of, not the stage's -- and
// rebuilds result around it. A condensation that does not measure smaller
// than the assembly it would replace is refused: accepting it could raise
// TokensUsed, and a post-assembly pass that grows the context is a failed
// condensation however plausible its text.
func (c *Composer) applyCondensed(ctx context.Context, result ComposedResult, class, condensed string) ComposedResult {
	n, err := c.counter.Count(ctx, condensed)
	if err != nil {
		c.publishDegrade(ctx, stagePositionPost, class, stageReasonCountFailed, err)
		return result
	}
	if n >= result.TokensUsed {
		c.publishDegrade(ctx, stagePositionPost, class, stageReasonCondenseRejected, nil)
		return result
	}
	return ComposedResult{
		Slots: []ComposedSlot{{
			Kind:       result.Slots[0].Kind,
			Label:      postStageCondensedLabel,
			Content:    condensed,
			Tokens:     n,
			Summarized: true,
		}},
		TokensUsed: n,
		Budget:     result.Budget,
		Trims:      result.Trims,
	}
}

// publishDegrade reports one StageDegradedEvent. A nil publisher drops it;
// cause contributes only its taxonomy Kind, never its message, so no
// provider or content detail can reach a subscriber through this path.
func (c *Composer) publishDegrade(ctx context.Context, stage, class, reason string, cause error) {
	if c.events == nil {
		return
	}
	kind := ""
	if k, ok := cascade.KindOf(cause); ok {
		kind = k.String()
	}
	c.events.Publish(ctx, StageEvent{Degraded: &StageDegradedEvent{
		Stage: stage, TaskClass: class, Reason: reason, Kind: kind,
	}})
}

// cloneSlots copies slots so a stage that rewrites its working set cannot
// mutate the caller's own slice or its elements. Compose promises the
// caller's input is read-only, and a transforming stage is exactly the case
// that would otherwise break that promise.
func cloneSlots(slots []Slot) []Slot {
	out := make([]Slot, len(slots))
	copy(out, slots)
	return out
}

// pipelineInputFromSlots joins every non-empty Slot's Content, in caller
// order, with a newline. The pre-assembly renders call it as their
// emptiness test (pipeline_transform.go, pipeline_segment.go): an empty
// join means there is nothing to classify or segment.
func pipelineInputFromSlots(slots []Slot) string {
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		if s.Content != "" {
			parts = append(parts, s.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// pipelineInputFromComposed is pipelineInputFromSlots' twin over the
// surviving ComposedSlots, and shapes its output IDENTICALLY: the same
// newline join, and the same skip of an empty Content (an earlier revision
// kept empty entries here and dropped them there, so the two helpers
// disagreed about what "the assembled text" was). The condensation pass
// runs over what Compose actually kept, not over the caller's original,
// possibly larger, slot list.
func pipelineInputFromComposed(slots []ComposedSlot) string {
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		if s.Content != "" {
			parts = append(parts, s.Content)
		}
	}
	return strings.Join(parts, "\n")
}
