package citations

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

func TestLineRangeKnown(t *testing.T) {
	cases := []struct {
		name string
		r    LineRange
		want bool
	}{
		{"zero value is unknown", LineRange{}, false},
		{"line zero is not a line", LineRange{Start: 0, End: 4}, false},
		{"negative start is not a line", LineRange{Start: -1, End: 4}, false},
		{"end before start is not a span", LineRange{Start: 9, End: 4}, false},
		{"single line", LineRange{Start: 7, End: 7}, true},
		{"multi line", LineRange{Start: 1, End: 40}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.Known(); got != c.want {
				t.Fatalf("Known() = %v, want %v for %+v", got, c.want, c.r)
			}
		})
	}
}

func TestLineRangeOverlaps(t *testing.T) {
	cases := []struct {
		name string
		a, b LineRange
		want bool
	}{
		{"identical", LineRange{1, 10}, LineRange{1, 10}, true},
		{"contained", LineRange{1, 10}, LineRange{4, 5}, true},
		{"touching at one line", LineRange{1, 10}, LineRange{10, 20}, true},
		{"adjacent but disjoint", LineRange{1, 9}, LineRange{10, 20}, false},
		{"far apart", LineRange{1, 2}, LineRange{80, 90}, false},
		{"unknown never overlaps a known span", LineRange{}, LineRange{1, 10}, false},
		{"a known span never overlaps an unknown one", LineRange{1, 10}, LineRange{}, false},
		{"two unknowns do not overlap each other", LineRange{}, LineRange{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Overlaps(c.b); got != c.want {
				t.Fatalf("Overlaps() = %v, want %v", got, c.want)
			}
			if got := c.b.Overlaps(c.a); got != c.want {
				t.Fatalf("Overlaps() is not symmetric: reversed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLineRangeUnionCoversBoth(t *testing.T) {
	got := LineRange{Start: 10, End: 20}.union(LineRange{Start: 4, End: 12})
	want := LineRange{Start: 4, End: 20}
	if got != want {
		t.Fatalf("union = %+v, want %+v", got, want)
	}
	if got := want.union(LineRange{Start: 6, End: 9}); got != want {
		t.Fatalf("union with a contained span changed it: %+v", got)
	}
}

func TestLeastTrustNeverReportsMoreTrustThanItWasGiven(t *testing.T) {
	cases := []struct {
		name string
		a, b corpus.TrustLevel
		want corpus.TrustLevel
	}{
		{"both trusted", corpus.TrustTrusted, corpus.TrustTrusted, corpus.TrustTrusted},
		{"trusted meets untrusted", corpus.TrustTrusted, corpus.TrustUntrustedSource, corpus.TrustUntrustedSource},
		{"untrusted meets trusted", corpus.TrustUntrustedSource, corpus.TrustTrusted, corpus.TrustUntrustedSource},
		{"both untrusted", corpus.TrustUntrustedSource, corpus.TrustUntrustedSource, corpus.TrustUntrustedSource},
		{"unset collapses to untrusted", "", corpus.TrustTrusted, corpus.TrustUntrustedSource},
		{"unrecognized collapses to untrusted", corpus.TrustLevel("vouched"), corpus.TrustTrusted, corpus.TrustUntrustedSource},
		{"two unreadable values collapse to untrusted", "", corpus.TrustLevel("x"), corpus.TrustUntrustedSource},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := leastTrust(c.a, c.b); got != c.want {
				t.Fatalf("leastTrust(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestLeastTrustAgreesWithTheCorpusModel checks this package's restrictive
// combination against the corpus model's own, observed through the surface
// the corpus package actually exposes rather than through a copy of its
// internal ordering. A trusted record inside an untrusted corpus surfaces
// as untrusted there; leastTrust must reach the same answer, because a
// citation combining them differently would be the two rules drifting.
func TestLeastTrustAgreesWithTheCorpusModel(t *testing.T) {
	store := corpus.NewStore()
	c := corpus.Corpus{
		ID: "fetched", ScopeRef: "proj/a",
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityPrivate,
		Trust: corpus.TrustUntrustedSource,
	}
	if err := store.AddCorpus(c); err != nil {
		t.Fatalf("AddCorpus: %v", err)
	}
	r := corpus.Record{
		ID: "chunk-1", CorpusID: "fetched", ScopeRef: "proj/a",
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityPrivate,
		Trust: corpus.TrustTrusted,
	}
	if err := store.AddRecord(r); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	got, err := store.Query(corpus.Query{
		Membership:  corpus.Membership{Scope: "proj/a"},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d records, want 1", len(got))
	}
	if got[0].Trust != corpus.TrustUntrustedSource {
		t.Fatalf("corpus model resolved trust to %q, want untrusted-source", got[0].Trust)
	}
	if combined := leastTrust(r.Trust, c.Trust); combined != got[0].Trust {
		t.Fatalf("leastTrust = %q but the corpus model resolved %q", combined, got[0].Trust)
	}
}

func TestMergeableRequiresSameFileAndOverlappingKnownSpans(t *testing.T) {
	base := Citation{Path: "a.go", Lines: LineRange{10, 20}}
	cases := []struct {
		name  string
		other Citation
		want  bool
	}{
		{"same file, overlapping", Citation{Path: "a.go", Lines: LineRange{18, 30}}, true},
		{"same file, disjoint", Citation{Path: "a.go", Lines: LineRange{40, 50}}, false},
		{"different file, same lines", Citation{Path: "b.go", Lines: LineRange{10, 20}}, false},
		{"no line information", Citation{Path: "a.go"}, false},
		{"no path on either side", Citation{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := base.mergeable(c.other); got != c.want {
				t.Fatalf("mergeable = %v, want %v", got, c.want)
			}
		})
	}
	pathless := Citation{Lines: LineRange{10, 20}}
	if pathless.mergeable(Citation{Lines: LineRange{10, 20}}) {
		t.Fatal("two pathless citations merged; a citation with no path names no file to share")
	}
}

func TestMergeKeepsTheStrongestHalfAndCombinesTheRest(t *testing.T) {
	strong := Citation{
		ChunkID: "chunk-strong", Path: "a.go", Lines: LineRange{10, 20},
		CorpusID: "docs", Trust: corpus.TrustTrusted,
		Rank: 2, Score: 0.9, RawScore: 0.031,
		Strategies: []rrf.StrategyName{rrf.StrategyVector},
	}
	weak := Citation{
		ChunkID: "chunk-weak", Path: "a.go", Lines: LineRange{18, 34},
		CorpusID: "docs", Trust: corpus.TrustTrusted,
		Rank: 5, Score: 0.4, RawScore: 0.016,
		Strategies: []rrf.StrategyName{rrf.StrategyFTS},
	}
	for _, order := range []struct {
		name string
		a, b Citation
	}{
		{"strong first", strong, weak},
		{"weak first", weak, strong},
	} {
		t.Run(order.name, func(t *testing.T) {
			got := order.a.merge(order.b)
			if got.ChunkID != "chunk-strong" {
				t.Fatalf("merged citation reports chunk %q; the strongest contributor was chunk-strong", got.ChunkID)
			}
			if got.Rank != 2 || got.Score != 0.9 || got.RawScore != 0.031 {
				t.Fatalf("merged citation kept rank %d score %v raw %v, want the strongest half's 2/0.9/0.031",
					got.Rank, got.Score, got.RawScore)
			}
			if got.Lines != (LineRange{10, 34}) {
				t.Fatalf("merged span %+v, want the union 10-34", got.Lines)
			}
			want := []rrf.StrategyName{rrf.StrategyFTS, rrf.StrategyVector}
			if len(got.Strategies) != 2 || got.Strategies[0] != want[0] || got.Strategies[1] != want[1] {
				t.Fatalf("merged strategies %v, want the sorted union %v", got.Strategies, want)
			}
			if len(got.MergedChunkIDs) != 1 || got.MergedChunkIDs[0] != "chunk-weak" {
				t.Fatalf("merged chunk ids %v, want [chunk-weak]", got.MergedChunkIDs)
			}
		})
	}
}

func TestMergeTakesTheLeastTrustedHalf(t *testing.T) {
	trusted := Citation{
		ChunkID: "a", Path: "a.go", Lines: LineRange{1, 10},
		CorpusID: "docs", Trust: corpus.TrustTrusted, Rank: 1, Score: 1,
	}
	untrusted := Citation{
		ChunkID: "b", Path: "a.go", Lines: LineRange{5, 12},
		CorpusID: "docs", Trust: corpus.TrustUntrustedSource, Rank: 4, Score: 0.2,
	}
	got := trusted.merge(untrusted)
	if got.Trust != corpus.TrustUntrustedSource {
		t.Fatalf("merged trust %q: the merge laundered an untrusted source into a trusted citation", got.Trust)
	}
	if got.ChunkID != "a" || got.Rank != 1 {
		t.Fatalf("merged identity %s rank %d, want the stronger half a/1", got.ChunkID, got.Rank)
	}
}

func TestMergeChunkIDsAreSortedAndExcludeThePrimary(t *testing.T) {
	a := Citation{ChunkID: "m", Path: "a.go", Lines: LineRange{1, 10}, Rank: 1, MergedChunkIDs: []string{"z"}}
	b := Citation{ChunkID: "c", Path: "a.go", Lines: LineRange{4, 12}, Rank: 3}
	got := a.merge(b)
	want := []string{"c", "z"}
	if len(got.MergedChunkIDs) != len(want) {
		t.Fatalf("merged chunk ids %v, want %v", got.MergedChunkIDs, want)
	}
	for i := range want {
		if got.MergedChunkIDs[i] != want[i] {
			t.Fatalf("merged chunk ids %v, want %v", got.MergedChunkIDs, want)
		}
	}
}
