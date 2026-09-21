package context

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the SEGMENT pre-assembly transform (P1-E21-W5-S46-T3): its
//   prompt, the parser for the offset protocol that prompt states, and the
//   split that replaces one over-long slot with the parts the model's
//   offsets cut it into. Separate from pipeline_transform.go only because
//   the two together exceed Art.10.3's 300-line file cap; the offset
//   protocol is the one self-contained half, so it is the half that moved.
// Inputs: the StageInput being transformed and one model response string.
// Outputs: a rewritten StageInput.Slots, or a fixed rejection reason.
// Constraints: NO PARTIAL SPLIT. One bad offset anywhere rejects the whole
//   response and leaves every slot whole, because a caller cannot tell an
//   assembly that was half-segmented from one that was segmented as asked.
// SPORT: context-engine/pipeline-segment (ADD, P1-E21-W5-S46-T3).

// The fixed reasons this transform rejects its own response with.
const (
	stageRejectSegmentShape   = "the segment response did not return one boundary line per slot"
	stageRejectSegmentOffsets = "a returned split offset fell outside its slot"
)

// pipelineSegmentPrompt states the offset protocol parseSegmentBoundaries
// enforces, including the "0 means do not split" answer.
const pipelineSegmentPrompt = "Each numbered section below is a block of text. For every section, choose the " +
	"character offsets at which it splits into coherent parts. Answer with exactly %d lines, one per section " +
	"in order, each formatted as <section number>:<offset>[,<offset>...], using offset 0 for a section that " +
	"should not be split, and nothing else.\n\n%s"

// renderSegmentPrompt implements stageRender for the segment stage, with
// the same empty-input guard renderClassifyPrompt documents.
func renderSegmentPrompt(in *StageInput) (string, error) {
	if pipelineInputFromSlots(in.Slots) == "" {
		return "", errPipelineEmptyInput(pipelineTaskClassSegment)
	}
	sections, n := numberedSections(in.Slots)
	return fmt.Sprintf(pipelineSegmentPrompt, n, sections), nil
}

// applySegment implements stageTransform for the segment stage: every slot
// longer than its equal share of the input is replaced by the parts the
// model's offsets cut it into. A slot at or under its share is left whole
// even when offsets were returned for it -- segmentation exists to stop one
// slot from crowding out the rest, and splitting a slot that is not crowding
// anything only fragments the assembly.
func applySegment(_ context.Context, in *StageInput, output string, _ provider.TokenCounter) (string, error) {
	sections := nonEmptyContents(in.Slots)
	boundaries, ok := parseSegmentBoundaries(output, len(sections))
	if !ok {
		return stageRejectSegmentShape, nil
	}
	if !boundariesInRange(boundaries, sections) {
		return stageRejectSegmentOffsets, nil
	}
	in.Slots = splitOverShareSlots(in.Slots, boundaries, shareOf(sections))
	return "", nil
}

// parseSegmentBoundaries parses the segment protocol: exactly want lines,
// the i-th of them prefixed with section number i, followed by a
// comma-separated offset list. Anything else is unparsable, which is a
// rejection, never a guess at what the model meant.
func parseSegmentBoundaries(output string, want int) ([][]int, bool) {
	lines := responseLines(output)
	if want == 0 || len(lines) != want {
		return nil, false
	}
	out := make([][]int, want)
	for i, line := range lines {
		head, tail, found := strings.Cut(line, ":")
		if !found {
			return nil, false
		}
		if n, err := strconv.Atoi(strings.TrimSpace(head)); err != nil || n != i+1 {
			return nil, false
		}
		offsets, ok := parseOffsetList(tail)
		if !ok {
			return nil, false
		}
		out[i] = offsets
	}
	return out, true
}

// parseOffsetList parses one comma-separated offset list. A single 0 is the
// protocol's "do not split" answer and yields an empty list; a 0 mixed in
// with real offsets is malformed rather than ignorable.
func parseOffsetList(tail string) ([]int, bool) {
	fields := strings.Split(tail, ",")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 0 {
			return nil, false
		}
		if n == 0 {
			if len(fields) != 1 {
				return nil, false
			}
			return nil, true
		}
		out = append(out, n)
	}
	return out, true
}

// boundariesInRange reports whether every section's offsets are strictly
// increasing and strictly inside that section's own rune length. An offset
// at 0 or at the end would cut off an empty part, and an offset past the end
// names a position that does not exist.
func boundariesInRange(boundaries [][]int, sections []string) bool {
	for i, offsets := range boundaries {
		limit := len([]rune(sections[i]))
		prev := 0
		for _, o := range offsets {
			if o <= prev || o >= limit {
				return false
			}
			prev = o
		}
	}
	return true
}

// shareOf returns one section's equal share of the total input length, in
// runes. Every section holds at least one rune (nonEmptyContents built the
// slice), so the quotient is at least 1; the empty guard is only there
// because a zero divisor would panic, and applySegment has already returned
// before it could reach this with no sections.
func shareOf(sections []string) int {
	if len(sections) == 0 {
		return 1
	}
	total := 0
	for _, s := range sections {
		total += len([]rune(s))
	}
	return total / len(sections)
}

// splitOverShareSlots rebuilds the slot list, replacing each over-share slot
// that has offsets with the parts those offsets cut it into and carrying
// every other slot through untouched.
func splitOverShareSlots(slots []Slot, boundaries [][]int, share int) []Slot {
	out := make([]Slot, 0, len(slots))
	section := 0
	for _, s := range slots {
		if s.Content == "" {
			out = append(out, s)
			continue
		}
		offsets := boundaries[section]
		section++
		runes := []rune(s.Content)
		if len(offsets) == 0 || len(runes) <= share {
			out = append(out, s)
			continue
		}
		out = append(out, splitSlotAt(s, runes, offsets)...)
	}
	return out
}

// splitSlotAt cuts one slot into the parts offsets name. Each part keeps the
// original Kind and ClassLabel and carries the original Label with a part
// ordinal appended -- an ordinal is a caller-chosen label plus a number, so a
// BudgetTrimEvent naming a part still names no content.
func splitSlotAt(s Slot, runes []rune, offsets []int) []Slot {
	cuts := append(append(make([]int, 0, len(offsets)+2), 0), offsets...)
	cuts = append(cuts, len(runes))
	parts := make([]Slot, 0, len(cuts)-1)
	for i := 0; i+1 < len(cuts); i++ {
		parts = append(parts, Slot{
			Kind:       s.Kind,
			Label:      fmt.Sprintf("%s#%d", s.Label, i+1),
			Content:    string(runes[cuts[i]:cuts[i+1]]),
			ClassLabel: s.ClassLabel,
		})
	}
	return parts
}
