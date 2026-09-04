package rrf

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// TestNormalizeScores_DegenerateInputs asserts each degenerate case
// separately rather than assuming the formula covers them. Every one of
// these occurs in practice: a query that matched nothing, a query that
// matched once, and a set whose scores are all equal (any set of chunks
// holding mirrored ranks across equally weighted legs).
func TestNormalizeScores_DegenerateInputs(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want []float64
	}{
		{"empty", []float64{}, []float64{}},
		{"nil", nil, []float64{}},
		{"single", []float64{0.42}, []float64{1}},
		{"single zero", []float64{0}, []float64{1}},
		{"all identical", []float64{0.5, 0.5, 0.5}, []float64{1, 1, 1}},
		{"two values", []float64{1, 3}, []float64{0, 1}},
		{"three spread", []float64{10, 6, 2}, []float64{1, 0.5, 0}},
		{"all negative", []float64{-2, -6, -10}, []float64{1, 0.5, 0}},
		{"spanning zero", []float64{4, 0, -4}, []float64{1, 0.5, 0}},
		{"all identical negative", []float64{-7, -7}, []float64{1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeScores(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d scores, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if math.Abs(got[i]-tt.want[i]) > tolerance {
					t.Errorf("score %d = %v, want %v", i, got[i], tt.want[i])
				}
				if got[i] < 0 || got[i] > 1 {
					t.Errorf("score %d = %v, outside [0,1]", i, got[i])
				}
			}
		})
	}
}

// TestFuse_NormalizationOverTheFusedSet checks that normalization is
// applied once, over the fused list, rather than per leg before fusion.
// Applied per leg it could not put the fused best at 1.0.
func TestFuse_NormalizationOverTheFusedSet(t *testing.T) {
	out := mustFuse(t, []RankedList{
		leg("alpha", "a", "b", "c"),
		leg("beta", "a", "b", "c"),
	}, DefaultK)
	if out[0].Score != 1 {
		t.Errorf("best result scored %v, want 1", out[0].Score)
	}
	if out[len(out)-1].Score != 0 {
		t.Errorf("worst result scored %v, want 0", out[len(out)-1].Score)
	}
	for i := 1; i < len(out); i++ {
		if out[i].Score >= out[i-1].Score {
			t.Errorf("normalization reordered results: %v then %v", out[i-1], out[i])
		}
		if out[i].RawScore >= out[i-1].RawScore {
			t.Errorf("raw order is not descending: %v then %v", out[i-1], out[i])
		}
	}
}

func TestFuse_SingleResultNormalizesToOne(t *testing.T) {
	out := mustFuse(t, []RankedList{leg("alpha", "only")}, DefaultK)
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	if out[0].Score != 1 {
		t.Errorf("Score = %v, want 1", out[0].Score)
	}
	if out[0].RawScore == out[0].Score {
		t.Log("raw and normalized scores coincide here only by accident of the value")
	}
}

// TestFuse_AllTiedNormalizeToOne is the zero-range case reaching the
// public surface: chunks fused to exactly equal scores are all equally
// best, and reporting them as 0 would say the opposite.
func TestFuse_AllTiedNormalizeToOne(t *testing.T) {
	out := mustFuse(t, []RankedList{
		leg("alpha", "a", "b"),
		leg("beta", "b", "a"),
	}, DefaultK)
	if len(out) != 2 || out[0].RawScore != out[1].RawScore {
		t.Fatalf("this case needs an exactly tied set to assert anything: %v", out)
	}
	for _, r := range out {
		if r.Score != 1 {
			t.Errorf("%s scored %v, want 1 (every result here ties exactly)", r.ChunkID, r.Score)
		}
	}
}

// TestFuse_DedupeIdentityIsChunkID states what identity means and proves
// the merged result keeps the summed score rather than the first or last
// contribution seen.
func TestFuse_DedupeIdentityIsChunkID(t *testing.T) {
	shared := "chunk-shared"
	out := mustFuse(t, []RankedList{
		{Strategy: StrategyFTS, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: shared, Path: "b/second.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
			{ChunkID: "chunk-fts-only", Path: "c.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
		}},
		{Strategy: StrategyVector, Weight: NeutralWeight, Hits: []Candidate{
			{ChunkID: "chunk-vec-only", Path: "d.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
			{ChunkID: shared, Path: "a/first.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
		}},
	}, DefaultK)

	var seen int
	var merged FusedResult
	for _, r := range out {
		if r.ChunkID == shared {
			seen++
			merged = r
		}
	}
	if seen != 1 {
		t.Fatalf("chunk %s produced %d results, want exactly one", shared, seen)
	}
	want := 1.0/61.0 + 1.0/62.0
	if math.Abs(merged.RawScore-want) > tolerance {
		t.Errorf("merged score = %v, want the sum of both contributions %v", merged.RawScore, want)
	}
	if merged.RawScore <= 1.0/61.0 {
		t.Error("the merged score is no better than one contribution alone, so the other was dropped")
	}
	if len(merged.Strategies) != 2 {
		t.Errorf("strategies = %v, want both legs", merged.Strategies)
	}
	if merged.Path != "a/first.md" {
		t.Errorf("Path = %q, want the deterministic choice among the observed paths", merged.Path)
	}
	if out[0].ChunkID != shared {
		t.Errorf("top result = %s, want the chunk both legs returned", out[0].ChunkID)
	}
}

// TestFuse_TrustPropagation checks the tag rides through untouched, and
// that a disagreement between legs resolves to the untrusted side rather
// than to whichever leg was read first.
func TestFuse_TrustPropagation(t *testing.T) {
	tests := []struct {
		name  string
		left  corpus.TrustLevel
		right corpus.TrustLevel
		want  corpus.TrustLevel
	}{
		{"both trusted", corpus.TrustTrusted, corpus.TrustTrusted, corpus.TrustTrusted},
		{"both untrusted", corpus.TrustUntrustedSource, corpus.TrustUntrustedSource, corpus.TrustUntrustedSource},
		{"trusted then untrusted", corpus.TrustTrusted, corpus.TrustUntrustedSource, corpus.TrustUntrustedSource},
		{"untrusted then trusted", corpus.TrustUntrustedSource, corpus.TrustTrusted, corpus.TrustUntrustedSource},
		{"unset on one side", corpus.TrustTrusted, corpus.TrustLevel(""), corpus.TrustUntrustedSource},
		{"unrecognized value", corpus.TrustLevel("probably-fine"), corpus.TrustLevel("probably-fine"),
			corpus.TrustUntrustedSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mustFuse(t, []RankedList{
				{Strategy: StrategyFTS, Weight: NeutralWeight,
					Hits: []Candidate{{ChunkID: "x", Trust: tt.left}}},
				{Strategy: StrategyVector, Weight: NeutralWeight,
					Hits: []Candidate{{ChunkID: "x", Trust: tt.right}}},
			}, DefaultK)
			if out[0].Trust != tt.want {
				t.Errorf("Trust = %q, want %q", out[0].Trust, tt.want)
			}
		})
	}
}

// golden mirrors one harvested v1 fusion case.
type golden struct {
	Name  string `json:"name"`
	K     int64  `json:"k"`
	Lists []struct {
		Strategy string   `json:"strategy"`
		Weight   float64  `json:"weight"`
		Hits     []string `json:"hits"`
	} `json:"lists"`
	Expected []struct {
		ChunkID    string   `json:"chunk_id"`
		RawScore   float64  `json:"raw_score"`
		Strategies []string `json:"strategies"`
	} `json:"expected"`
}

// TestFuse_V1Goldens replays the archived implementation's own asserted
// fusion cases. The expected values are v1's, not this package's output,
// so a change in ranking behaviour fails here instead of quietly becoming
// the new baseline.
func TestFuse_V1Goldens(t *testing.T) {
	dir := filepath.Join("testdata", "v1-goldens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading goldens: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no golden fixtures present")
	}
	for _, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("reading %s: %v", e.Name(), err)
			}
			var g golden
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatalf("parsing %s: %v", e.Name(), err)
			}
			assertGolden(t, g)
		})
	}
}

func assertGolden(t *testing.T, g golden) {
	t.Helper()
	lists := make([]RankedList, 0, len(g.Lists))
	for _, l := range g.Lists {
		lists = append(lists, RankedList{
			Strategy: StrategyName(l.Strategy),
			Weight:   l.Weight,
			Hits:     hits(l.Hits...),
		})
	}
	out, err := Fuse(lists, g.K)
	if err != nil {
		t.Fatalf("%s: %v", g.Name, err)
	}
	if len(out) != len(g.Expected) {
		t.Fatalf("%s: fused %d results, v1 asserts %d", g.Name, len(out), len(g.Expected))
	}
	for i, want := range g.Expected {
		if out[i].ChunkID != want.ChunkID {
			t.Errorf("%s: rank %d = %s, v1 asserts %s", g.Name, i+1, out[i].ChunkID, want.ChunkID)
		}
		if math.Abs(out[i].RawScore-want.RawScore) > 1e-9 {
			t.Errorf("%s: %s scored %v, v1 asserts %v", g.Name, want.ChunkID, out[i].RawScore, want.RawScore)
		}
		if len(out[i].Strategies) != len(want.Strategies) {
			t.Errorf("%s: %s contributed by %v, v1 asserts %v",
				g.Name, want.ChunkID, out[i].Strategies, want.Strategies)
			continue
		}
		for j := range want.Strategies {
			if string(out[i].Strategies[j]) != want.Strategies[j] {
				t.Errorf("%s: %s contributed by %v, v1 asserts %v",
					g.Name, want.ChunkID, out[i].Strategies, want.Strategies)
				break
			}
		}
	}
	for _, r := range out {
		if r.Score < 0 || r.Score > 1 {
			t.Errorf("%s: %s normalized to %v, outside [0,1]", g.Name, r.ChunkID, r.Score)
		}
	}
}

// TestFuse_ZeroWeightLegStillRecorded checks that a disabled leg
// contributes no score but is not silently rewritten into never having
// run: it is still named as a contributing strategy.
func TestFuse_ZeroWeightLegStillRecorded(t *testing.T) {
	out := mustFuse(t, []RankedList{
		{Strategy: "alpha", Weight: 0, Hits: hits("x")},
		leg("beta", "x"),
	}, DefaultK)
	if math.Abs(out[0].RawScore-1.0/61.0) > tolerance {
		t.Errorf("score = %v, want only the neutral leg's contribution", out[0].RawScore)
	}
	if len(out[0].Strategies) != 2 {
		t.Errorf("strategies = %v, want both legs recorded", out[0].Strategies)
	}
}
