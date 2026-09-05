package retrieval_test

// Purpose: the query operators' behaviour against the real index —
//   exclusion, phrase adjacency, and the integrity refusal a corrupt row
//   produces. These are the paths where a wrong answer is a disclosure
//   rather than a nuisance: an exclusion that did not exclude, or a phrase
//   that matched a document merely holding both words, returns documents
//   the caller's query said to leave out.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — real driver, TempDir, no clock, no network.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newOperatorHarness indexes three documents that differ only in ways the
// operators can distinguish.
func newOperatorHarness(t *testing.T) *ftsHarness {
	t.Helper()
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	h.indexDoc(t, ftsHandbook, "adjacent.md", "the rank fusion merges lists")
	h.indexDoc(t, ftsHandbook, "apart.md", "the rank of a leg and the fusion of its lists")
	h.indexDoc(t, ftsHandbook, "excluded.md", "the rank fusion is a vector merge")
	return h
}

// TestFTS5ExclusionExcludes: a negated term removes a document that would
// otherwise match, and removes only that document.
func TestFTS5ExclusionExcludes(t *testing.T) {
	h := newOperatorHarness(t)
	if got := len(h.search(t, ftsSession, "rank fusion", 10)); got != 3 {
		t.Fatalf("the plain query matched %d documents, want all 3", got)
	}
	got := paths(h.search(t, ftsSession, "rank fusion -vector", 10))
	for _, p := range got {
		if p == "excluded.md" {
			t.Errorf("the excluded document is still in the answer: %v", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("exclusion left %d documents (%v), want 2", len(got), got)
	}
}

// TestFTS5PhraseNeedsAdjacency: a quoted phrase matches only where the
// tokens are actually adjacent in that order. A document holding both
// words far apart must not match, or the quotes would be decoration on a
// conjunction.
func TestFTS5PhraseNeedsAdjacency(t *testing.T) {
	got := paths(newOperatorHarness(t).search(t, ftsSession, `"rank fusion"`, 10))
	for _, p := range got {
		if p == "apart.md" {
			t.Errorf("a document holding the words apart matched the phrase: %v", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("the phrase matched %d documents (%v), want the 2 that carry it adjacently", len(got), got)
	}
	if reversed := paths(newOperatorHarness(t).search(t, ftsSession, `"fusion rank"`, 10)); len(reversed) != 0 {
		t.Errorf("the reversed phrase matched %v; a phrase is ordered", reversed)
	}
}

// TestFTS5CorruptRowRefuses: a row that cannot be read whole is an
// integrity refusal, not a silently short answer. Asserted by corrupting a
// row in the real store underneath a real query.
func TestFTS5CorruptRowRefuses(t *testing.T) {
	h := newOperatorHarness(t)
	hits := h.search(t, ftsSession, "rank fusion", 10)
	if len(hits) == 0 {
		t.Fatal("nothing matched, so the corruption assertion would be vacuous")
	}
	ctx := context.Background()
	if err := h.store.Put(ctx, retrieval.IndexNamespace,
		"fts:doc:"+hits[0].ChunkID, []byte("{not a row")); err != nil {
		t.Fatalf("corrupting a row: %v", err)
	}
	_, err := h.index.Search(ctx, retrieval.SearchRequest{
		Text: "rank fusion", Scope: h.filter(t, ftsSession).Predicate(),
	}, 10)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("a corrupt row produced %v, want KindIntegrity rather than a short answer", err)
	}
}

// TestFTS5ReindexReplacesContent: re-indexing a chunk id with different
// content retracts the old postings, so a term only the old content
// carried stops answering.
func TestFTS5ReindexReplacesContent(t *testing.T) {
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	chunk := h.indexDoc(t, ftsHandbook, "a.md", "quokka rank fusion")
	if got := h.search(t, ftsSession, "quokka", 10); len(got) != 1 {
		t.Fatalf("the original term matched %d documents, want 1", len(got))
	}
	if err := h.index.Write(context.Background(), ftsHandbook.ID, []retrieval.Chunk{
		{ID: chunk.ID, Path: "a.md", Content: []byte("rank fusion only")},
	}); err != nil {
		t.Fatalf("re-Write: %v", err)
	}
	if got := h.search(t, ftsSession, "quokka", 10); len(got) != 0 {
		t.Errorf("a term only the replaced content carried still answers: %v", paths(got))
	}
	if got := h.search(t, ftsSession, "rank fusion", 10); len(got) != 1 {
		t.Errorf("the replacement content does not answer: %v", paths(got))
	}
}
