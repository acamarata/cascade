package context

import (
	"context"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the classify and summarize transforms pipeline.go's stages
//   perform, each paired with the prompt that states the response protocol
//   it parses, plus the section-rendering helpers all three transforms
//   share (the segment transform's own protocol lives in
//   pipeline_segment.go). Prompt and parser live side by side on purpose --
//   they are two halves of one protocol, and a protocol whose halves live in
//   different files drifts.
// Inputs: the StageInput being transformed and one model response string.
// Outputs: the transformed StageInput, or a fixed rejection reason (never
//   derived from the response) when the response did not honor the protocol.
// Constraints: a rejected response leaves the working set EXACTLY as it
//   arrived -- partial application is worse than none, because a caller
//   cannot tell a half-labelled assembly from a fully-labelled one. No
//   parser here ever puts model output into an error or an event.
// SPORT: context-engine/pipeline-transform (ADD, P1-E21-W5-S46-T3).

// The fixed reasons the classify and summarize transforms reject their own
// response with. They travel as StageInput.Degraded and reach an operator as
// StageDegradedEvent.Reason.
const (
	stageRejectClassifyShape       = "the classify response did not return one label per slot"
	stageRejectClassifyLabel       = "a returned label was longer than a slot label may be"
	stageRejectSummarizeEmpty      = "the condensation came back empty"
	stageRejectSummarizeNotShorter = "the condensation was not shorter than the assembled context"
)

// classifyMaxLabelRunes bounds a returned label. A label is a category word
// that rides along on every composed slot; a long one is a model answering
// with a sentence (or with the slot's own content) rather than with a
// category, which is a failed classification, not a long label.
const classifyMaxLabelRunes = 32

// The two prompts owned here. Each states its response protocol explicitly,
// because the parser enforces that protocol exactly and degrades rather than
// guessing at a response shaped some other way.
const (
	pipelineClassifyPrompt = "Give each numbered section below a one-word category label. " +
		"Answer with exactly %d lines, one label per line, in section order, and nothing else.\n\n%s"
	pipelineSummarizePrompt = "Condense the assembled context below, preserving key facts and decisions. " +
		"Your answer must be shorter than the input. Answer with the condensed text only.\n\n%s"
)

// renderClassifyPrompt implements stageRender for the classify stage and
// owns that stage's empty-input guard: with nothing to classify there is
// nothing to dispatch, and an empty prompt would be a wasted call.
func renderClassifyPrompt(in *StageInput) (string, error) {
	if pipelineInputFromSlots(in.Slots) == "" {
		return "", errPipelineEmptyInput(pipelineTaskClassClassify)
	}
	sections, n := numberedSections(in.Slots)
	return fmt.Sprintf(pipelineClassifyPrompt, n, sections), nil
}

// renderSummarizePrompt implements stageRender for the post-assembly stage,
// over the assembled text the composer put in Text.
func renderSummarizePrompt(in *StageInput) (string, error) {
	if in.Text == "" {
		return "", errPipelineEmptyInput(pipelineTaskClassSummarize)
	}
	return fmt.Sprintf(pipelineSummarizePrompt, in.Text), nil
}

// numberedSections renders every non-empty slot's Content as a numbered
// section, 1-based over the non-empty slots only, and returns the rendering
// plus how many sections it holds. The numbering is what makes a response
// line addressable back to a slot.
func numberedSections(slots []Slot) (string, int) {
	var b strings.Builder
	n := 0
	for _, s := range slots {
		if s.Content == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s\n", n, s.Content)
	}
	return b.String(), n
}

// nonEmptyContents returns the Content of every non-empty slot, in slot
// order: the section list a response's lines are matched against.
func nonEmptyContents(slots []Slot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		if s.Content != "" {
			out = append(out, s.Content)
		}
	}
	return out
}

// responseLines splits a model response into trimmed, non-blank lines.
func responseLines(output string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// applyClassify implements stageTransform for the classify stage: one label
// per non-empty slot, written onto that slot's ClassLabel so the composer
// can carry it onto the result.
func applyClassify(_ context.Context, in *StageInput, output string, _ provider.TokenCounter) (string, error) {
	labels := responseLines(output)
	if len(labels) != len(nonEmptyContents(in.Slots)) {
		return stageRejectClassifyShape, nil
	}
	for _, label := range labels {
		if len([]rune(label)) > classifyMaxLabelRunes {
			return stageRejectClassifyLabel, nil
		}
	}
	next := 0
	for i := range in.Slots {
		if in.Slots[i].Content == "" {
			continue
		}
		in.Slots[i].ClassLabel = labels[next]
		next++
	}
	return "", nil
}

// applySummarize implements stageTransform for the post-assembly stage: the
// condensation replaces the assembled text only when this stage's own
// counter says it is genuinely smaller.
func applySummarize(ctx context.Context, in *StageInput, output string, counter provider.TokenCounter) (string, error) {
	condensed := strings.TrimSpace(output)
	if condensed == "" {
		return stageRejectSummarizeEmpty, nil
	}
	after, err := counter.Count(ctx, condensed)
	if err != nil {
		return "", errPipelineMeasure(err)
	}
	before, err := counter.Count(ctx, in.Text)
	if err != nil {
		return "", errPipelineMeasure(err)
	}
	if after >= before {
		return stageRejectSummarizeNotShorter, nil
	}
	in.Text = condensed
	return "", nil
}
