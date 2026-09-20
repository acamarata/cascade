// Package topics (hysteresis_test.go): Purpose: the hysteresis rule, at
// both levels it has to be right at - confirm() in isolation (construction
// validation, the confirmation window, flicker rejection, and the
// unobservable tail window), and the boundary INDICES a real Segmenter
// emits for the four scenarios that define the rule: a single clean topic
// change, flicker, two changes, and a change on the last turn. The index
// assertions are explicit numbers, because the defect these tests exist to
// pin was a rule that reported the right number of boundaries at the wrong
// turns.
package topics

import (
	"context"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHysteresisFilter_InvalidConfig(t *testing.T) {
	for name, cfg := range map[string]HysteresisConfig{
		"zero window":    {Threshold: 0.5, Window: 0},
		"zero threshold": {Threshold: 0, Window: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newHysteresisFilter(cfg); !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("newHysteresisFilter(%+v): error = %v, want KindInvalidInput", cfg, err)
			}
		})
	}
}

// TestHysteresisFilter_Confirm drives confirm() directly over
// hand-written spike maps. spikes[i] is the transition into turn i, so
// index 0 is always false and `candidate` is the index of the first turn
// of the proposed new segment.
func TestHysteresisFilter_Confirm(t *testing.T) {
	cases := []struct {
		name      string
		window    int
		spikes    []bool
		candidate int
		want      bool
	}{
		{"window 1 commits on the candidate spike alone", 1, []bool{false, true, true}, 1, true},
		{"quiet confirmation window commits", 2, []bool{false, true, false}, 1, true},
		{"counter-spike inside the window is flicker", 2, []bool{false, true, true}, 1, false},
		{"spike past the window does not reject", 2, []bool{false, true, false, true}, 1, true},
		{"late counter-spike inside a longer window rejects", 3, []bool{false, true, false, true, false}, 1, false},
		{"window running past the last transition commits provisionally", 3, []bool{false, false, true}, 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := newHysteresisFilter(HysteresisConfig{Threshold: 0.5, Window: c.window})
			if err != nil {
				t.Fatalf("newHysteresisFilter: %v", err)
			}
			if got := f.confirm(c.spikes, c.candidate); got != c.want {
				t.Fatalf("confirm(%v, %d) at Window=%d = %v, want %v", c.spikes, c.candidate, c.window, got, c.want)
			}
		})
	}
}

// The four scenario fixtures. Vectors are 2-wide to match testEmbedModel
// (segmenter_core_test.go): {1,0} and {0,1} are orthogonal (distance 1,
// above the 0.5 threshold every scenario uses), {-1,0} is opposite {1,0}
// (distance 2) and orthogonal to {0,1}, and a repeated vector is distance
// 0, so each fixture's spike pattern is exact rather than approximate.
var (
	cleanChangeLabels  = []string{"A", "A", "B", "B"}
	cleanChangeVectors = [][]float32{{1, 0}, {1, 0}, {0, 1}, {0, 1}}

	flickerLabels  = []string{"A", "A", "B", "A", "A"}
	flickerVectors = [][]float32{{1, 0}, {1, 0}, {0, 1}, {1, 0}, {1, 0}}

	twoChangeLabels  = []string{"A", "A", "B", "B", "C", "C"}
	twoChangeVectors = [][]float32{{1, 0}, {1, 0}, {0, 1}, {0, 1}, {-1, 0}, {-1, 0}}

	tailChangeLabels  = []string{"A", "A", "B"}
	tailChangeVectors = [][]float32{{1, 0}, {1, 0}, {0, 1}}
)

// segmentIndices runs a real Segmenter over labels/vectors at the given
// Window and returns the committed boundary indices, so a scenario asserts
// on numbers rather than on a struct dump.
func segmentIndices(t *testing.T, window int, labels []string, vectors [][]float32) []int {
	t.Helper()
	exec := &fakeClassifyExecutor{labels: labels}
	emb := &fakeEmbedder{vectors: vectors}
	s, err := NewSegmenter(exec, emb, HysteresisConfig{Threshold: 0.5, Window: window})
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	bounds, err := s.Segment(context.Background(), turnsN(len(labels)))
	if err != nil {
		t.Fatalf("Segment: %v", err)
	}
	idxs := make([]int, len(bounds))
	for i, b := range bounds {
		idxs[i] = b.TurnIndex
	}
	return idxs
}

func assertIndices(t *testing.T, what string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got boundaries at %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got boundaries at %v, want %v", what, got, want)
		}
	}
}

// TestSegmenterHysteresisScenarios pins the four defining cases. The
// expected index is in every case the FIRST TURN OF THE NEW TOPIC, not the
// turn at which the confirmation window finished: turn 2 for the clean
// change (labels A,A,B,B), turns 2 and 4 for the two-change window, turn 2
// for the change on the last turn (provisional - its confirmation window
// runs past the end), and nothing at all for the flicker, where the single
// B turn is answered by an immediate counter-spike back to A.
func TestSegmenterHysteresisScenarios(t *testing.T) {
	cases := []struct {
		name    string
		labels  []string
		vectors [][]float32
		want    []int
	}{
		{"single clean change", cleanChangeLabels, cleanChangeVectors, []int{2}},
		{"flicker is rejected", flickerLabels, flickerVectors, nil},
		{"two changes", twoChangeLabels, twoChangeVectors, []int{2, 4}},
		{"change on the last turn commits provisionally", tailChangeLabels, tailChangeVectors, []int{2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertIndices(t, c.name, segmentIndices(t, 2, c.labels, c.vectors), c.want)
		})
	}
}

// TestSegmenterBoundaryIndexDoesNotMoveWithWindow is the regression test for
// the late-index defect: the clean-change fixture's boundary is at turn 2
// under every Window, because Window controls how much evidence a
// candidate needs, never which turn is reported. A rule that counted N
// consecutive spikes instead reports turn 2 at Window=1, turn 3 at
// Window=2, and nothing at Window=3 on this same fixture.
func TestSegmenterBoundaryIndexDoesNotMoveWithWindow(t *testing.T) {
	for _, window := range []int{1, 2, 3} {
		got := segmentIndices(t, window, cleanChangeLabels, cleanChangeVectors)
		assertIndices(t, "clean change at Window="+strconv.Itoa(window), got, []int{2})
	}
}

// TestSegmenterFlickerSurvivesOnlyWithHysteresisOff is the companion
// mutation proof: the flicker fixture that yields no boundary at Window=2
// yields BOTH the excursion (turn 2) and the return (turn 3) at Window=1.
// The two assertions cannot both pass unless confirm()'s window is
// actually consulted, so neither test can be satisfied by a filter that
// always commits or always refuses.
func TestSegmenterFlickerSurvivesOnlyWithHysteresisOff(t *testing.T) {
	assertIndices(t, "flicker at Window=1", segmentIndices(t, 1, flickerLabels, flickerVectors), []int{2, 3})
	assertIndices(t, "flicker at Window=2", segmentIndices(t, 2, flickerLabels, flickerVectors), nil)
}
