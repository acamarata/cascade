// Purpose: the reranker stage's ordering tests — that reordering moves
// rows without editing them, and that the resulting order is a function
// of the scores alone: ties break on ascending chunk id, the same rule
// fusion uses, so the order cannot depend on which sequence the reranker
// happened to list its ties in.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.1.1 — SpyReranker is declared in reranker_test.go.
// Art.7 — no network, no files, no clock.
//
// SPORT: internal.retrieval.rrf.Rerank/ADDED (P1-E06-W2-S12-T3).

package rrf

import (
	"context"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestRerankerPreservesTrustAndProvenance asserts that reordering moves
// rows and edits nothing: each reordered row is the row fusion produced,
// with its own trust tag, corpus, path and fused scores. Nothing is
// laundered by changing position.
func TestRerankerPreservesTrustAndProvenance(t *testing.T) {
	before := map[string]FusedResult{}
	for _, r := range fusedFixture() {
		before[r.ChunkID] = r
	}
	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(&SpyReranker{}))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(out.Results) != len(before) {
		t.Fatalf("got %d results, want %d", len(out.Results), len(before))
	}
	for _, got := range out.Results {
		want, ok := before[got.ChunkID]
		if !ok {
			t.Fatalf("unknown chunk %q in output", got.ChunkID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("chunk %q was altered by reranking:\n got %+v\nwant %+v", got.ChunkID, got, want)
		}
	}
}

// TestRerankerTiesBreakOnChunkIDDeterministically pins requirement 5: a
// reranker that scores everything equally, and lists its ties in a
// different order each call, still yields one specific ordering — the
// same ascending-chunk-id rule fusion uses.
func TestRerankerTiesBreakOnChunkIDDeterministically(t *testing.T) {
	forward := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		out := make([]provider.RankedPassage, 0, len(passages))
		for _, p := range passages {
			out = append(out, provider.RankedPassage{Text: p, Score: 1})
		}
		return out
	}}
	reversed := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		out := make([]provider.RankedPassage, 0, len(passages))
		for i := len(passages) - 1; i >= 0; i-- {
			out = append(out, provider.RankedPassage{Text: passages[i], Score: 1})
		}
		return out
	}}

	first, err := Rerank(context.Background(), "q", fusedFixture(), enabled(forward))
	if err != nil {
		t.Fatalf("Rerank (forward): %v", err)
	}
	second, err := Rerank(context.Background(), "q", fusedFixture(), enabled(reversed))
	if err != nil {
		t.Fatalf("Rerank (reversed): %v", err)
	}
	if !reflect.DeepEqual(first.Results, second.Results) {
		t.Errorf("tie order depended on the reranker's listing order:\n%v\n%v",
			chunkIDs(first.Results), chunkIDs(second.Results))
	}
	if got, want := chunkIDs(first.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tie order = %v, want ascending chunk id %v", got, want)
	}
}

// TestRerankerRepeatedRunsAreIdentical is determinism end to end: the
// same deterministic reranker over the same input, twice.
func TestRerankerRepeatedRunsAreIdentical(t *testing.T) {
	a, err := Rerank(context.Background(), "q", fusedFixture(), enabled(&SpyReranker{}))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	b, err := Rerank(context.Background(), "q", fusedFixture(), enabled(&SpyReranker{}))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !reflect.DeepEqual(a.Results, b.Results) {
		t.Errorf("two identical runs diverged:\n%+v\n%+v", a.Results, b.Results)
	}
}
