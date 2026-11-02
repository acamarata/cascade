package retrieval_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestFTS5UnicodeQueries(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	chunk := h.indexDoc(t, ftsHandbook, "unicode.md", "東京 中文 😀 cafe\u0301 مرحبا שלום quoted fusion")
	for _, tc := range []struct {
		query  string
		refuse bool
		count  int
	}{
		{"東京", true, 0}, {"中文", true, 0}, {"😀", true, 0}, {"مرحبا", true, 0}, {"שלום", true, 0},
		{"cafe\u0301", false, 1}, {"東京 fusion", true, 0}, {`"quoted fusion"`, false, 1},
		{`quoted"fusion"`, false, 0}, {`fusion"`, true, 0}, {`"😀"`, true, 0},
		{`"quoted" "fusion"`, false, 1},
	} {
		t.Run(tc.query, func(t *testing.T) {
			hits, err := h.index.Search(context.Background(), retrieval.SearchRequest{
				Text: tc.query, Scope: h.filter(t, ftsSession).Predicate(),
			}, 10)
			if tc.refuse {
				if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
					t.Fatalf("want typed invalid-input refusal, hits=%v err=%v", hits, err)
				}
				if len(hits) != 0 {
					t.Fatalf("refused query returned hits: %+v", hits)
				}
				return
			}
			if err != nil || len(hits) != tc.count || (len(hits) > 0 && hits[0].ChunkID != chunk.ID) {
				t.Fatalf("query returned hits=%+v err=%v", hits, err)
			}
		})
	}
}
