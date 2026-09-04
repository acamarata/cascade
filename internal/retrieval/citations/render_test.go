package citations

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

func renderableSet() CitationSet {
	return CitationSet{Citations: []Citation{
		{
			ChunkID: "c1", Path: "docs/a.md", Lines: LineRange{Start: 10, End: 24},
			CorpusID: "docs", Trust: corpus.TrustTrusted, Rank: 1, Score: 1,
			Strategies: []rrf.StrategyName{rrf.StrategyFTS, rrf.StrategyVector},
		},
		{
			ChunkID: "c2", Path: "docs/b.md", CorpusID: "docs",
			Trust: corpus.TrustTrusted, Rank: 4, Score: 0.4275,
		},
	}}
}

func TestRenderProducesReferenceAndDefinitionForms(t *testing.T) {
	got := Render(renderableSet())
	wantRefs := []string{"[^1]", "[^2]"}
	if !reflect.DeepEqual(got.Refs, wantRefs) {
		t.Fatalf("refs %v, want %v", got.Refs, wantRefs)
	}
	want := "[^1]: docs/a.md lines 10-24 (score: 1.000)\n" +
		"[^2]: docs/b.md (score: 0.427)\n"
	if got.Definitions != want {
		t.Fatalf("definitions:\n%q\nwant:\n%q", got.Definitions, want)
	}
}

// TestRenderNumbersFootnotesByPositionNotRank keeps the two numbering
// systems apart. Retrieval ranks have gaps wherever a merge or a withheld
// result removed a row, and a footnote block numbered 1, 4 is not valid
// Markdown footnote output.
func TestRenderNumbersFootnotesByPositionNotRank(t *testing.T) {
	got := Render(renderableSet())
	if !strings.Contains(got.Definitions, "[^2]: docs/b.md") {
		t.Fatalf("the second citation (retrieval rank 4) is not footnote 2:\n%s", got.Definitions)
	}
	if strings.Contains(got.Definitions, "[^4]") {
		t.Fatalf("a retrieval rank leaked into the footnote numbering:\n%s", got.Definitions)
	}
	if len(got.Refs) != 2 || got.Refs[1] != "[^2]" {
		t.Fatalf("refs %v, want the definitions' own numbering", got.Refs)
	}
}

func TestRenderOmitsWhatTheCitationDoesNotKnow(t *testing.T) {
	cases := []struct {
		name string
		c    Citation
		want string
	}{
		{
			name: "no line span",
			c:    Citation{ChunkID: "c", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
			want: "[^1]: a.md (score: 0.000)\n",
		},
		{
			name: "no path falls back to the corpus",
			c:    Citation{ChunkID: "c", CorpusID: "memories", Trust: corpus.TrustTrusted, Score: 0.5},
			want: "[^1]: corpus memories (score: 0.500)\n",
		},
		{
			name: "no path and no corpus falls back to the chunk id",
			c:    Citation{ChunkID: "abc123", Trust: corpus.TrustTrusted, Score: 0.25},
			want: "[^1]: chunk abc123 (score: 0.250)\n",
		},
		{
			name: "an unusable span is not repaired into a line clause",
			c: Citation{ChunkID: "c", Path: "a.md", CorpusID: "docs",
				Trust: corpus.TrustTrusted, Lines: LineRange{Start: 0, End: 12}},
			want: "[^1]: a.md (score: 0.000)\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Render(CitationSet{Citations: []Citation{c.c}})
			if got.Definitions != c.want {
				t.Fatalf("definitions %q, want %q", got.Definitions, c.want)
			}
		})
	}
}

func TestRenderMarksEveryCitationThatIsNotTrusted(t *testing.T) {
	for _, trust := range []corpus.TrustLevel{
		corpus.TrustUntrustedSource, "", corpus.TrustLevel("vouched"),
	} {
		set := CitationSet{Citations: []Citation{
			{ChunkID: "c", Path: "a.md", CorpusID: "docs", Trust: trust, Score: 1},
		}}
		got := Render(set).Definitions
		if !strings.HasSuffix(got, untrustedMarker+"\n") {
			t.Fatalf("trust %q rendered as %q, want the untrusted marker", trust, got)
		}
	}
	trusted := Render(CitationSet{Citations: []Citation{
		{ChunkID: "c", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 1},
	}}).Definitions
	if strings.Contains(trusted, untrustedMarker) {
		t.Fatalf("a trusted citation rendered the untrusted marker: %q", trusted)
	}
}

func TestRenderEmptySetRendersNothing(t *testing.T) {
	for _, set := range []CitationSet{{}, {Citations: []Citation{}, Withheld: 3}} {
		got := Render(set)
		if len(got.Refs) != 0 || got.Definitions != "" {
			t.Fatalf("empty set rendered %+v", got)
		}
	}
}

// TestRenderIsByteIdentical asserts the property the --cite output and the
// MCP responses both depend on: the same set renders the same bytes every
// time, on every run.
func TestRenderIsByteIdentical(t *testing.T) {
	want := Render(renderableSet())
	for i := 0; i < 100; i++ {
		got := Render(renderableSet())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d rendered %+v, want %+v", i, got, want)
		}
	}
}

// TestRenderScoreFormatIsFixedWidth pins the score format. An adaptive
// format would render a very small RRF score in exponential notation,
// which a reader compares against the others as if it were a large one.
func TestRenderScoreFormatIsFixedWidth(t *testing.T) {
	set := CitationSet{Citations: []Citation{
		{ChunkID: "c", Path: "a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.0000123},
	}}
	got := Render(set).Definitions
	if !strings.Contains(got, "(score: 0.000)") {
		t.Fatalf("definitions %q, want a fixed-width score", got)
	}
	if strings.Contains(got, "e-") {
		t.Fatalf("definitions %q rendered a score in exponential form", got)
	}
}
