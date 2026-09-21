// Package topics (segmenter_types_test.go): Purpose: the two claims
// segmenter_types.go's doc comments make about Boundary, each checked
// against real Segmenter output rather than against a struct literal or a
// stub: (1) TurnIndex and Label describe the FIRST TURN of the new segment,
// and (2) Boundary's shape mirrors eval.go's CorpusRecord closely enough
// that an EvalPredictions is built from a []Boundary with no intermediate
// translation type.
package topics

import (
	"context"
	"testing"
)

// TestSegmenterBoundaryNamesTheFirstTurnOfTheNewSegment drives the clean-change
// fixture (hysteresis_test.go) through a real Segmenter and asserts both
// fields against the input: turn 2 is where the vector jumped and where the
// classifier first said "B", so the Boundary must carry exactly that index
// and that label. A Boundary reported at the confirmation-window's end
// would still be "one boundary with label B" and would fail here.
func TestSegmenterBoundaryNamesTheFirstTurnOfTheNewSegment(t *testing.T) {
	exec := &fakeClassifyExecutor{labels: cleanChangeLabels}
	emb := &fakeEmbedder{vectors: cleanChangeVectors}
	s, err := newTestSegmenter(exec, emb, validCfg())
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	bounds, err := s.Segment(context.Background(), turnsN(len(cleanChangeLabels)))
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	if len(bounds) != 1 {
		t.Fatalf("got %d boundaries (%v), want exactly 1", len(bounds), bounds)
	}
	if bounds[0].TurnIndex != 2 {
		t.Fatalf("TurnIndex = %d, want 2 (the first turn labeled B, where the distance spiked)", bounds[0].TurnIndex)
	}
	if bounds[0].Label != cleanChangeLabels[2] {
		t.Fatalf("Label = %q, want %q (the classifier's label AT the boundary turn)",
			bounds[0].Label, cleanChangeLabels[2])
	}
}

// TestSegmenterBoundariesScoreAgainstACorpusRecord is the mirror-shape
// claim, made falsifiable: a record whose labeled boundary is turn 2 is
// scored against the segmenter's own []Boundary with no translation step,
// and must come out at F1 1.0. If Boundary's TurnIndex ever stopped
// meaning "corpus boundary index", this scores 0 rather than compiling
// happily.
func TestSegmenterBoundariesScoreAgainstACorpusRecord(t *testing.T) {
	rec := CorpusRecord{
		ID:          "mirror-shape",
		Turns:       turnsN(len(cleanChangeLabels)),
		Boundaries:  []int{2},
		TopicLabels: map[int]string{0: "A", 1: "A", 2: "B", 3: "B"},
	}
	s, err := newTestSegmenter(&fakeClassifyExecutor{labels: cleanChangeLabels},
		&fakeEmbedder{vectors: cleanChangeVectors}, validCfg())
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	bounds, err := s.Segment(context.Background(), rec.Turns)
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	idxs := make([]int, len(bounds))
	for i, b := range bounds {
		idxs[i] = b.TurnIndex
	}
	result := Evaluate(Corpus{rec}, EvalPredictions{Boundaries: map[string][]int{rec.ID: idxs}})
	if result.BoundaryF1 != 1.0 {
		t.Fatalf("boundary F1 = %v (precision %v, recall %v) scoring %v against labeled boundaries %v, want 1.0",
			result.BoundaryF1, result.BoundaryPrecision, result.BoundaryRecall, idxs, rec.Boundaries)
	}
}
