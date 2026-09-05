package retrieval_test

// Purpose: the full-text index's behaviour tests — round-trip, idempotent
//   re-index, deletion, ranking order, determinism, and the scope
//   guarantee, all against a real SQLite database.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — real driver, TempDir, no clock, no network.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ftsCorpus is the handbook's content. fusion.md holds the query's terms
// most often and must therefore rank first; storage.md holds none of them
// and must not appear at all.
var ftsCorpus = map[string]string{
	"fusion.md": "# Fusion\n\nReciprocal rank fusion merges the ranked lists each " +
		"leg returns into one rank order. Rank fusion is the merge.\n",
	"recall.md": "# Recall\n\nA reciprocal rank fusion query returns cited chunks.\n",
	"storage.md": "# Storage\n\nThe driver keeps chunks and their vectors; it " +
		"neither ranks nor merges.\n",
}

// ftsSecret is the out-of-scope corpus, written to be the STRONGEST match
// for the query so no scope assertion can pass by accident.
const ftsSecret = "# Private\n\nquokka reciprocal rank fusion reciprocal rank fusion " +
	"reciprocal rank fusion quokka.\n"

const ftsQuery = "reciprocal rank fusion"

// newIndexedHarness ingests both corpora.
func newIndexedHarness(t *testing.T) *ftsHarness {
	t.Helper()
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	h.addCorpus(t, ftsJournal)
	for _, name := range []string{"fusion.md", "recall.md", "storage.md"} {
		h.indexDoc(t, ftsHandbook, name, ftsCorpus[name])
	}
	h.indexDoc(t, ftsJournal, "secrets.md", ftsSecret)
	return h
}

// TestFTS5RoundTripRanksBM25 is the acceptance case: real chunks in, a
// ranked answer out, with the document that best answers the query first
// and the document that does not answer it at all absent.
func TestFTS5RoundTripRanksBM25(t *testing.T) {
	h := newIndexedHarness(t)
	hits := h.search(t, ftsSession, ftsQuery, 10)

	if len(hits) != 2 {
		t.Fatalf("query returned %d hits (%v), want the two documents that carry every term",
			len(hits), paths(hits))
	}
	if hits[0].Path != "fusion.md" {
		t.Errorf("top hit is %q, want fusion.md (it carries the query's terms most often)", hits[0].Path)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("rank 1 scored %v and rank 2 scored %v; the order must follow the score",
			hits[0].Score, hits[1].Score)
	}
	if hits[0].CorpusID != ftsHandbook.ID {
		t.Errorf("hit carries corpus %q, want %q", hits[0].CorpusID, ftsHandbook.ID)
	}
	if !strings.Contains(strings.ToLower(hits[0].Snippet), "fusion") {
		t.Errorf("snippet does not show the matched term: %q", hits[0].Snippet)
	}
}

// TestFTS5ScopeHolds is the property most likely to break: the
// unauthorized corpus holds the strongest match for the query, so a leak
// would win the ranking rather than hide at the bottom of it.
func TestFTS5ScopeHolds(t *testing.T) {
	h := newIndexedHarness(t)
	hits := h.search(t, ftsSession, ftsQuery, 10)

	if len(hits) == 0 {
		t.Fatal("the query returned nothing, so the scope assertion would be vacuous")
	}
	for _, hit := range hits {
		if hit.CorpusID == ftsJournal.ID || hit.Path == "secrets.md" ||
			strings.Contains(strings.ToLower(hit.Snippet), "quokka") {
			t.Errorf("out-of-scope content leaked into the ranking: %+v", hit)
		}
	}
}

// TestFTS5ScopeAdmitsItsOwnCorpus is the control for the test above: the
// journal's own session does get the journal, so the leak test is not
// passing merely because the index returns nothing.
func TestFTS5ScopeAdmitsItsOwnCorpus(t *testing.T) {
	h := newIndexedHarness(t)
	hits := h.search(t, corpusQueryFor(ftsJournal), ftsQuery, 10)

	if len(hits) != 1 || hits[0].Path != "secrets.md" {
		t.Fatalf("the journal's own session did not receive its own corpus: %v", paths(hits))
	}
}

// TestFTS5EmptyScopeReturnsNothing: a session authorized to read nothing
// gets nothing. A scope narrowing that fell back to "no narrowing" would
// return the whole index here.
func TestFTS5EmptyScopeReturnsNothing(t *testing.T) {
	h := newIndexedHarness(t)
	hits, err := h.index.Search(context.Background(), retrieval.SearchRequest{
		Text: ftsQuery, Scope: emptyScope(),
	}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("an unauthorized session received %d hits: %v", len(hits), paths(hits))
	}
}

// TestFTS5IdempotentUpsert: indexing the same chunk twice leaves exactly
// one document row and one posting per token, counted in the store itself
// rather than through the query path.
func TestFTS5IdempotentUpsert(t *testing.T) {
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	chunk := h.indexDoc(t, ftsHandbook, "fusion.md", ftsCorpus["fusion.md"])

	docs, postings := h.countRows(t, "fts:doc:"), h.countRows(t, "fts:tok:")
	if err := h.index.Write(context.Background(), ftsHandbook.ID,
		[]retrieval.Chunk{{ID: chunk.ID, Path: chunk.Path, Content: chunk.Content}}); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	if got := h.countRows(t, "fts:doc:"); got != docs || got != 1 {
		t.Errorf("re-indexing one chunk left %d document rows, want exactly 1", got)
	}
	if got := h.countRows(t, "fts:tok:"); got != postings {
		t.Errorf("re-indexing left %d postings, want the original %d", got, postings)
	}
	if hits := h.search(t, ftsSession, ftsQuery, 10); len(hits) != 1 {
		t.Errorf("re-indexed chunk answers %d times, want once: %v", len(hits), paths(hits))
	}
}

// TestFTS5DeleteRemovesFromIndex: a retired chunk stops answering, and
// leaves no posting behind that could answer later. A stale posting that
// still answers is exactly what a retirement is supposed to make
// impossible.
func TestFTS5DeleteRemovesFromIndex(t *testing.T) {
	h := newIndexedHarness(t)
	before := h.search(t, ftsSession, ftsQuery, 10)
	if len(before) == 0 {
		t.Fatal("nothing matched before the delete, so the assertion would be vacuous")
	}

	if err := h.index.Delete(context.Background(), []string{before[0].ChunkID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, hit := range h.search(t, ftsSession, ftsQuery, 10) {
		if hit.ChunkID == before[0].ChunkID {
			t.Fatalf("the deleted chunk still answers the query: %+v", hit)
		}
	}
	if n := h.countRows(t, "fts:tok:reciprocal:"+before[0].ChunkID); n != 0 {
		t.Errorf("%d postings survived the delete; a stale posting still answers", n)
	}
	if err := h.index.Delete(context.Background(), []string{before[0].ChunkID}); err != nil {
		t.Errorf("deleting an absent chunk must be a no-op, got %v", err)
	}
}

// TestFTS5Deterministic: identical corpus and query rank identically,
// including across two independently built indexes.
func TestFTS5Deterministic(t *testing.T) {
	first := newIndexedHarness(t).search(t, ftsSession, ftsQuery, 10)
	second := newIndexedHarness(t).search(t, ftsSession, ftsQuery, 10)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two independently built indexes ranked differently:\n%+v\n%+v", first, second)
	}
}

// TestFTS5EmptyIndexIsNotAnError: a query against an index holding nothing
// returns an empty result and no error, which is a different condition
// from a broken index.
func TestFTS5EmptyIndexIsNotAnError(t *testing.T) {
	h := newFTSHarness(t)
	h.addCorpus(t, ftsHandbook)
	hits, err := h.index.Search(context.Background(), retrieval.SearchRequest{
		Text: ftsQuery, Scope: h.filter(t, ftsSession).Predicate(),
	}, 10)
	if err != nil {
		t.Fatalf("querying an empty index must not be an error, got %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("an empty index returned %d hits", len(hits))
	}
}

// TestFTS5WriteRefusesUnusableInput covers the write path's error cases.
func TestFTS5WriteRefusesUnusableInput(t *testing.T) {
	h := newFTSHarness(t)
	ctx := context.Background()
	if err := h.index.Write(ctx, "", []retrieval.Chunk{{ID: "a", Content: []byte("x")}}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Write with no corpus id returned %v, want KindInvalidInput", err)
	}
	if err := h.index.Write(ctx, "c", []retrieval.Chunk{{Path: "p.md", Content: []byte("x")}}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Write of an id-less chunk returned %v, want KindInvalidInput", err)
	}
	if err := h.index.Write(ctx, "c", nil); err != nil {
		t.Errorf("Write of no chunks must be a no-op, got %v", err)
	}
	if err := h.index.Delete(ctx, []string{""}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Delete of an empty id returned %v, want KindInvalidInput", err)
	}
	if _, err := retrieval.NewIndex(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("NewIndex(nil) returned %v, want KindInvalidInput", err)
	}
	if _, err := h.index.Search(ctx, retrieval.SearchRequest{Text: ftsQuery}, 0); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Search with topK 0 returned %v, want KindInvalidInput", err)
	}
}
