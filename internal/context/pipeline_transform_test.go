package context

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the three transforms' OUTPUT (P1-E21-W5-S46-T3). Each accepted
//   case asserts the working set actually changed -- labels present, one
//   long slot split into parts, the text condensed -- and each rejected case
//   asserts it did NOT change and that a fixed reason was recorded. A stage
//   whose Execute call were removed, or whose write-back were dropped, fails
//   every accepted case here.
// Constraints: Art.1 -- doubles from pipeline_test.go only.
// SPORT: context-engine/pipeline-transform-tests (ADD, P1-E21-W5-S46-T3).

// TestClassifyTransformLabelsEverySlot: the accepted case writes one label
// per non-empty slot and leaves empty slots alone.
func TestClassifyTransformLabelsEverySlot(t *testing.T) {
	in := &StageInput{Slots: []Slot{
		{Label: "a", Content: "alpha text"},
		{Label: "blank"},
		{Label: "b", Content: "beta text"},
	}}
	exec := &stageExecutor{replies: []string{"instruction\nhistory\n"}}
	if err := mustPreStage(t, PreStageClassify, exec).Execute(context.Background(), in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if in.Degraded != "" {
		t.Fatalf("Degraded = %q, want the response accepted", in.Degraded)
	}
	if in.Slots[0].ClassLabel != "instruction" || in.Slots[2].ClassLabel != "history" {
		t.Errorf("labels = %q / %q, want instruction / history",
			in.Slots[0].ClassLabel, in.Slots[2].ClassLabel)
	}
	if in.Slots[1].ClassLabel != "" {
		t.Errorf("the empty slot was labelled %q; only slots with content are sections", in.Slots[1].ClassLabel)
	}
}

// TestClassifyTransformRejections: a label count that does not match the
// section count, and a label long enough to be a sentence, each leave every
// slot unlabelled and record a fixed reason.
func TestClassifyTransformRejections(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"too few labels", "only-one", stageRejectClassifyShape},
		{"too many labels", "one\ntwo\nthree", stageRejectClassifyShape},
		{"a label that is really a sentence", "this is plainly not a one word category label at all\nb",
			stageRejectClassifyLabel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &StageInput{Slots: []Slot{{Content: "alpha"}, {Content: "beta"}}}
			exec := &stageExecutor{replies: []string{tc.reply}}
			if err := mustPreStage(t, PreStageClassify, exec).Execute(context.Background(), in); err != nil {
				t.Fatalf("Execute: %v (a rejected response is not a failure)", err)
			}
			if in.Degraded != tc.want {
				t.Errorf("Degraded = %q, want %q", in.Degraded, tc.want)
			}
			for i, s := range in.Slots {
				if s.ClassLabel != "" {
					t.Errorf("slot %d was labelled %q on a rejected response", i, s.ClassLabel)
				}
			}
		})
	}
}

// TestSegmentTransformSplitsTheOverShareSlot: the long slot becomes three
// parts whose contents rejoin to the original, and the short slot stays one.
func TestSegmentTransformSplitsTheOverShareSlot(t *testing.T) {
	long := "0123456789012345678901234567890123456789"
	in := &StageInput{Slots: []Slot{
		{Kind: SlotKindTier, Label: "long", Content: long},
		{Kind: SlotKindMemory, Label: "short", Content: "tiny"},
	}}
	exec := &stageExecutor{replies: []string{"1:10,20\n2:0\n"}}
	if err := mustPreStage(t, PreStageSegment, exec).Execute(context.Background(), in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if in.Degraded != "" {
		t.Fatalf("Degraded = %q, want the response accepted", in.Degraded)
	}
	if len(in.Slots) != 4 {
		t.Fatalf("slots = %d, want 4 (the long slot split into 3, the short one whole)", len(in.Slots))
	}
	rejoined := in.Slots[0].Content + in.Slots[1].Content + in.Slots[2].Content
	if rejoined != long {
		t.Errorf("the parts do not rejoin to the original content: %q", rejoined)
	}
	if in.Slots[0].Label != "long#1" || in.Slots[2].Label != "long#3" {
		t.Errorf("part labels = %q .. %q, want long#1 .. long#3", in.Slots[0].Label, in.Slots[2].Label)
	}
	if in.Slots[0].Kind != SlotKindTier {
		t.Errorf("part Kind = %v, want the original slot's kind", in.Slots[0].Kind)
	}
	if in.Slots[3].Label != "short" || in.Slots[3].Content != "tiny" {
		t.Errorf("the at-share slot changed: %+v", in.Slots[3])
	}
}

// TestSegmentTransformRejections: every unparsable or out-of-range response
// leaves the slot list byte-identical and records a fixed reason.
func TestSegmentTransformRejections(t *testing.T) {
	long := "0123456789012345678901234567890123456789"
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"prose instead of the protocol", "I split it after the first sentence.", stageRejectSegmentShape},
		{"wrong line count", "1:10\n2:0\n3:0", stageRejectSegmentShape},
		{"section numbers out of order", "2:10\n1:0", stageRejectSegmentShape},
		{"non-numeric offset", "1:ten\n2:0", stageRejectSegmentShape},
		{"zero mixed with real offsets", "1:0,10\n2:0", stageRejectSegmentShape},
		{"offset past the end of its slot", "1:999\n2:0", stageRejectSegmentOffsets},
		{"offsets not increasing", "1:20,10\n2:0", stageRejectSegmentOffsets},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &StageInput{Slots: []Slot{{Label: "long", Content: long}, {Label: "short", Content: "tiny"}}}
			exec := &stageExecutor{replies: []string{tc.reply}}
			if err := mustPreStage(t, PreStageSegment, exec).Execute(context.Background(), in); err != nil {
				t.Fatalf("Execute: %v (a rejected response is not a failure)", err)
			}
			if in.Degraded != tc.want {
				t.Errorf("Degraded = %q, want %q", in.Degraded, tc.want)
			}
			if len(in.Slots) != 2 || in.Slots[0].Content != long {
				t.Errorf("slots changed on a rejected response: %+v", in.Slots)
			}
		})
	}
}

// TestSummarizeTransformCondensesOnlyWhenShorter: the shorter answer replaces
// the text; an empty one, an equal-length one and a longer one do not.
func TestSummarizeTransformCondensesOnlyWhenShorter(t *testing.T) {
	assembled := "alpha beta gamma delta epsilon"
	cases := []struct {
		name     string
		reply    string
		wantText string
		wantWhy  string
	}{
		{"shorter is accepted", "alpha delta", "alpha delta", ""},
		{"empty is rejected", "   ", assembled, stageRejectSummarizeEmpty},
		{"equal length is rejected", assembled, assembled, stageRejectSummarizeNotShorter},
		{"longer is rejected", assembled + " and more besides", assembled, stageRejectSummarizeNotShorter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &StageInput{Text: assembled}
			exec := &stageExecutor{replies: []string{tc.reply}}
			if err := mustSummarizeStage(t, exec).Execute(context.Background(), in); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if in.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", in.Text, tc.wantText)
			}
			if in.Degraded != tc.wantWhy {
				t.Errorf("Degraded = %q, want %q", in.Degraded, tc.wantWhy)
			}
		})
	}
}

// TestSummarizeTransformCounterFailureIsAnError: the stage cannot decide
// whether a condensation is smaller without a working counter, so a counter
// failure is a stage failure (which the composer then degrades) rather than a
// silently accepted replacement.
func TestSummarizeTransformCounterFailureIsAnError(t *testing.T) {
	counter := erroringCounter{err: cascade.New(cascade.KindUnavailable, "the tokenizer is down")}
	stage, err := NewSummarizeStage(&stageExecutor{replies: []string{"short"}}, counter)
	if err != nil {
		t.Fatalf("NewSummarizeStage: %v", err)
	}
	in := &StageInput{Text: "alpha beta gamma"}
	if err := stage.Execute(context.Background(), in); err == nil {
		t.Fatal("a counter failure must surface as an error")
	}
	if in.Text != "alpha beta gamma" {
		t.Errorf("Text = %q, want it untouched when the measurement failed", in.Text)
	}
}
