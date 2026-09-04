// Purpose: params.go's tests — the default-k resolution, the weight
// application, the copy-not-mutate guarantee, and the refusal of a weight
// naming a leg that did not run.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: pure computation; no clock, no I/O.
// SPORT: internal.retrieval.rrf.FuseWith/ADDED (P1-E06-W2-S12-T4).

package rrf

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// paramsLists builds two legs that disagree about which chunk is best, so
// a weight change is visible in the fused order.
func paramsLists() []RankedList {
	return []RankedList{
		{Strategy: StrategyFTS, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: "aaa", Path: "a.md", CorpusID: "c", Trust: corpus.TrustTrusted},
			{ChunkID: "bbb", Path: "b.md", CorpusID: "c", Trust: corpus.TrustTrusted},
		}},
		{Strategy: StrategyVector, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: "bbb", Path: "b.md", CorpusID: "c", Trust: corpus.TrustTrusted},
			{ChunkID: "aaa", Path: "a.md", CorpusID: "c", Trust: corpus.TrustTrusted},
		}},
	}
}

// TestParamsEffectiveK: an unset or non-positive k resolves to DefaultK,
// a configured one is used as written.
func TestParamsEffectiveK(t *testing.T) {
	for name, tc := range map[string]struct {
		in   int64
		want int64
	}{
		"unset":    {0, DefaultK},
		"negative": {-1, DefaultK},
		"set":      {5, 5},
	} {
		t.Run(name, func(t *testing.T) {
			if got := (Params{K: tc.in}).EffectiveK(); got != tc.want {
				t.Errorf("EffectiveK() = %d, want %d", got, tc.want)
			}
		})
	}
}

// sameResult compares two fused results field by field, comparing the
// strategy slices element-wise so the comparison does not depend on the
// slices being the same backing array.
func sameResult(a, b FusedResult) bool {
	if a.ChunkID != b.ChunkID || a.Path != b.Path || a.CorpusID != b.CorpusID ||
		a.Trust != b.Trust || a.RawScore != b.RawScore || a.Score != b.Score {
		return false
	}
	if len(a.Strategies) != len(b.Strategies) {
		return false
	}
	for i := range a.Strategies {
		if a.Strategies[i] != b.Strategies[i] {
			return false
		}
	}
	return true
}

// TestFuseWithZeroParamsMatchesFuse: the zero Params is the shipped
// behaviour, not a special case. Fusing through the config hook with
// nothing configured must produce exactly what Fuse produces.
func TestFuseWithZeroParamsMatchesFuse(t *testing.T) {
	direct, err := Fuse(paramsLists(), DefaultK)
	if err != nil {
		t.Fatalf("Fuse: %v", err)
	}
	viaParams, err := FuseWith(paramsLists(), Params{})
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	if len(direct) != len(viaParams) {
		t.Fatalf("FuseWith returned %d results, Fuse returned %d", len(viaParams), len(direct))
	}
	for i := range direct {
		if !sameResult(direct[i], viaParams[i]) {
			t.Errorf("result %d: FuseWith = %+v, Fuse = %+v", i, viaParams[i], direct[i])
		}
	}
}

// TestFuseWithWeightsChangeOrder: a weight that favours one leg promotes
// that leg's top hit. This is the property the config surface exists for
// — a weights change the operator writes has to move the ranking.
func TestFuseWithWeightsChangeOrder(t *testing.T) {
	favourFTS, err := FuseWith(paramsLists(), Params{
		Weights: map[StrategyName]float64{StrategyFTS: 4, StrategyVector: 1},
	})
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	if favourFTS[0].ChunkID != "aaa" {
		t.Errorf("weighting fts5 up ranked %q first, want aaa", favourFTS[0].ChunkID)
	}
	favourVector, err := FuseWith(paramsLists(), Params{
		Weights: map[StrategyName]float64{StrategyFTS: 1, StrategyVector: 4},
	})
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	if favourVector[0].ChunkID != "bbb" {
		t.Errorf("weighting vector up ranked %q first, want bbb", favourVector[0].ChunkID)
	}
}

// TestFuseWithDoesNotMutateCallerLists: the caller may still hold the
// lists it passed, so the weight is written onto a copy.
func TestFuseWithDoesNotMutateCallerLists(t *testing.T) {
	lists := paramsLists()
	if _, err := FuseWith(lists, Params{
		Weights: map[StrategyName]float64{StrategyFTS: 9},
	}); err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	if lists[0].Weight != NeutralWeight {
		t.Errorf("caller's list weight became %v, want it left at %v", lists[0].Weight, NeutralWeight)
	}
}

// TestFuseWithUnknownLegRefused: a weight naming a leg that did not run is
// a hard error. Ignoring it would look identical, to the operator, to the
// weight having been applied.
func TestFuseWithUnknownLegRefused(t *testing.T) {
	_, err := FuseWith(paramsLists(), Params{
		Weights: map[StrategyName]float64{"lexical": 2},
	})
	if err == nil {
		t.Fatal("a weight naming an absent leg was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok %v), want %v", kind, ok, cascade.KindInvalidInput)
	}
}

// TestFuseWithNilListsAndWeightsRefused: weights over a nil list is the
// same caller bug Fuse refuses, refused at the same kind.
func TestFuseWithNilListsAndWeightsRefused(t *testing.T) {
	_, err := FuseWith(nil, Params{Weights: map[StrategyName]float64{StrategyFTS: 1}})
	if err == nil {
		t.Fatal("nil lists with weights was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok %v), want %v", kind, ok, cascade.KindInvalidInput)
	}
}

// TestFuseWithConfiguredKChangesScores: the configured k reaches Fuse's
// denominator, so two different k values produce two different scores.
func TestFuseWithConfiguredKChangesScores(t *testing.T) {
	small, err := FuseWith(paramsLists(), Params{K: 1})
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	large, err := FuseWith(paramsLists(), Params{K: 1000})
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	if small[0].RawScore == large[0].RawScore {
		t.Error("changing fusion.k did not change the fused score")
	}
}
