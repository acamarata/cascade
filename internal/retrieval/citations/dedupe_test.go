// This file holds the citation-level dedupe, the error paths and the
// determinism assertions for Assemble. It is separate from
// citationset_test.go only because the two together exceed the repository's
// 300-line file cap (Art.10.3); they are one suite over one entry point.

package citations

import (
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// overlappingResults are two chunks carved from the same file whose spans
// overlap, plus one chunk from a different file that must not be folded
// into them.
func overlappingResults() []rrf.FusedResult {
	return []rrf.FusedResult{
		{ChunkID: "a1", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
			Score: 1.0, RawScore: 0.032, Strategies: []rrf.StrategyName{rrf.StrategyVector}},
		{ChunkID: "b1", Path: "docs/b.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
			Score: 0.7, RawScore: 0.022, Strategies: []rrf.StrategyName{rrf.StrategyVector}},
		{ChunkID: "a2", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
			Score: 0.3, RawScore: 0.010, Strategies: []rrf.StrategyName{rrf.StrategyFTS}},
	}
}

func overlappingOptions() Options {
	return Options{
		Resolver: mapResolver{
			"a1": record("a1", "docs", corpus.TrustTrusted),
			"a2": record("a2", "docs", corpus.TrustTrusted),
			"b1": record("b1", "docs", corpus.TrustTrusted),
		},
		Locator: mapLocator{
			"a1": {Start: 10, End: 24},
			"a2": {Start: 20, End: 40},
			"b1": {Start: 10, End: 24},
		},
	}
}

func TestAssembleMergesOverlappingCitationsInTheSameFile(t *testing.T) {
	set := mustAssemble(t, overlappingResults(), overlappingOptions())
	if set.Len() != 2 {
		t.Fatalf("set holds %d citations, want 2 (the two same-file spans merged)", set.Len())
	}
	merged := set.Citations[0]
	if merged.ChunkID != "a1" || merged.Rank != 1 || merged.Score != 1.0 {
		t.Fatalf("merged citation is %s rank %d score %v, want the strongest half a1/1/1",
			merged.ChunkID, merged.Rank, merged.Score)
	}
	if merged.Lines != (LineRange{Start: 10, End: 40}) {
		t.Fatalf("merged span %+v, want the union 10-40", merged.Lines)
	}
	if len(merged.MergedChunkIDs) != 1 || merged.MergedChunkIDs[0] != "a2" {
		t.Fatalf("merged chunk ids %v, want [a2] — the citation must name every chunk it covers",
			merged.MergedChunkIDs)
	}
	if len(merged.Strategies) != 2 {
		t.Fatalf("merged strategies %v, want both contributing legs", merged.Strategies)
	}
	if set.Citations[1].ChunkID != "b1" || set.Citations[1].Path != "docs/b.md" {
		t.Fatalf("the other file's citation became %+v", set.Citations[1])
	}
}

func TestAssembleKeepsDisjointSpansInTheSameFileApart(t *testing.T) {
	opts := overlappingOptions()
	opts.Locator = mapLocator{
		"a1": {Start: 10, End: 24},
		"a2": {Start: 25, End: 40},
		"b1": {Start: 10, End: 24},
	}
	set := mustAssemble(t, overlappingResults(), opts)
	if set.Len() != 3 {
		t.Fatalf("set holds %d citations, want 3: two disjoint spans in one file are two citations", set.Len())
	}
}

func TestAssembleKeepsUnlocatedSameFileCitationsApart(t *testing.T) {
	set := mustAssemble(t, overlappingResults(), Options{Resolver: overlappingOptions().Resolver})
	if set.Len() != 3 {
		t.Fatalf("set holds %d citations, want 3: with no line information, "+
			"two chunks from one file have not been shown to be the same region", set.Len())
	}
}

func TestAssembleMergePreservesTheLeastTrustedHalf(t *testing.T) {
	results := overlappingResults()
	results[2].Trust = corpus.TrustUntrustedSource
	opts := overlappingOptions()
	opts.Resolver = mapResolver{
		"a1": record("a1", "docs", corpus.TrustTrusted),
		"a2": record("a2", "docs", corpus.TrustUntrustedSource),
		"b1": record("b1", "docs", corpus.TrustTrusted),
	}
	set := mustAssemble(t, results, opts)
	if set.Citations[0].Trust != corpus.TrustUntrustedSource {
		t.Fatalf("the merged citation reports %q; merging an untrusted chunk into a "+
			"trusted one must not report the trusted half", set.Citations[0].Trust)
	}
}

func TestAssembleEmptyResultSetCitesNothing(t *testing.T) {
	set, err := Assemble([]rrf.FusedResult{}, Options{Resolver: mapResolver{}})
	if err != nil {
		t.Fatalf("an empty result set is a query that found nothing, not an error: %v", err)
	}
	if set.Len() != 0 || set.Withheld != 0 {
		t.Fatalf("empty input produced %+v", set)
	}
	if r := Render(set); r.Definitions != "" || len(r.Refs) != 0 {
		t.Fatalf("empty set rendered %+v, want nothing at all", r)
	}
}

func TestAssembleErrorPaths(t *testing.T) {
	cases := []struct {
		name    string
		results []rrf.FusedResult
		opts    Options
		kind    cascade.Kind
	}{
		{
			name:    "nil result slice",
			results: nil,
			opts:    Options{Resolver: mapResolver{}},
			kind:    cascade.KindInvalidInput,
		},
		{
			name:    "no resolver",
			results: []rrf.FusedResult{{ChunkID: "c1"}},
			opts:    Options{},
			kind:    cascade.KindInvalidInput,
		},
		{
			name:    "result with no resolvable source identity",
			results: []rrf.FusedResult{{Path: "docs/a.md", CorpusID: "docs"}},
			opts:    Options{Resolver: mapResolver{}},
			kind:    cascade.KindInvalidInput,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, err := Assemble(c.results, c.opts)
			if err == nil {
				t.Fatalf("Assemble accepted %s and returned %+v", c.name, set)
			}
			if !cascade.HasKind(err, c.kind) {
				t.Fatalf("error is not a %v taxonomy error: %v", c.kind, err)
			}
			if set.Len() != 0 {
				t.Fatalf("a refused assembly still returned %d citations", set.Len())
			}
		})
	}
}

// TestAssembleRefusesConflictingCitationDedupe covers the third error
// path: two citations claim the same region of the same file from two
// different corpora. Merging them would attribute one corpus's content to
// the other, and picking a winner silently would hide that.
func TestAssembleRefusesConflictingCitationDedupe(t *testing.T) {
	results := []rrf.FusedResult{
		{ChunkID: "a1", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 1},
		{ChunkID: "a2", Path: "docs/a.md", CorpusID: "notes", Trust: corpus.TrustTrusted, Score: 0.4},
	}
	_, err := Assemble(results, Options{
		Resolver: mapResolver{
			"a1": record("a1", "docs", corpus.TrustTrusted),
			"a2": record("a2", "notes", corpus.TrustTrusted),
		},
		Locator: mapLocator{"a1": {Start: 1, End: 20}, "a2": {Start: 15, End: 30}},
	})
	if err == nil {
		t.Fatal("two corpora claiming one span merged silently")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("error is not a %v taxonomy error: %v", cascade.KindConflict, err)
	}
}

// TestAssembleIsDeterministic runs the same input repeatedly and requires
// byte-identical structure and rendering every time. Assemble holds maps
// only for lookups it never iterates, and this is the assertion that keeps
// it that way.
func TestAssembleIsDeterministic(t *testing.T) {
	first := mustAssemble(t, overlappingResults(), overlappingOptions())
	firstRender := Render(first)
	for i := 0; i < 50; i++ {
		got := mustAssemble(t, overlappingResults(), overlappingOptions())
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d produced %+v, want %+v", i, got, first)
		}
		if !reflect.DeepEqual(Render(got), firstRender) {
			t.Fatalf("run %d rendered differently:\n%s\nwant:\n%s",
				i, Render(got).Definitions, firstRender.Definitions)
		}
	}
}

// TestAssembleDoesNotMutateItsInput guards the caller's fused list: the
// recall surface renders the same results it cites, and a citation that
// rewrote a result's strategy slice would corrupt what the caller shows.
func TestAssembleDoesNotMutateItsInput(t *testing.T) {
	results := overlappingResults()
	before := overlappingResults()
	set := mustAssemble(t, results, overlappingOptions())
	if !reflect.DeepEqual(results, before) {
		t.Fatalf("Assemble mutated its input: %+v, want %+v", results, before)
	}
	set.Citations[0].Strategies[0] = "tampered"
	if results[0].Strategies[0] == "tampered" {
		t.Fatal("a citation shares its strategy slice with the fused result it describes")
	}
}
