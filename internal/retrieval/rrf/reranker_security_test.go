// Purpose: the reranker stage's adversarial tests — what happens when the
// reranker itself misbehaves. The stage treats a reranker reply as
// untrusted third-party output, and these are the specific abuses it must
// survive: a passage it was never given, a truncated reply, a duplicated
// one, an unusable score, and the legitimate-but-awkward case of two
// distinct chunks holding identical text.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.1.1 — SpyReranker is declared in reranker_test.go and
// used here; no reranker implementation exists in shipping code. Art.7 —
// no network, no files, no clock.
//
// SPORT: internal.retrieval.rrf.Rerank/ADDED (P1-E06-W2-S12-T3).

package rrf

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestRerankerCannotWidenVisibility is the security property. A reranker
// that returns a passage it was never given — a record scope resolution
// withheld from this session — must not be able to put it in front of
// the caller. The whole reply is refused and fusion order stands.
func TestRerankerCannotWidenVisibility(t *testing.T) {
	const withheld = "body of secret"
	spy := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		out := []provider.RankedPassage{{Text: withheld, Score: 99}}
		for i, p := range passages {
			out = append(out, provider.RankedPassage{Text: p, Score: float64(len(passages) - i)})
		}
		return out
	}}

	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if out.Applied {
		t.Error("a reply containing an unseen passage was applied")
	}
	if !cascade.HasKind(out.Degraded, cascade.KindIntegrity) {
		t.Errorf("Degraded = %v, want a KindIntegrity error", out.Degraded)
	}
	for _, r := range out.Results {
		if r.ChunkID == "secret" || textOf(r) == withheld {
			t.Fatalf("withheld record reached the caller through the reranker: %+v", r)
		}
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fallback order = %v, want fusion order %v", got, want)
	}
}

// TestRerankerCannotResurrectADroppedResult is the same property from the
// other side: a reranker cannot smuggle a record back by returning a
// truncated list and then a second one, because a reply that is not a
// complete permutation is refused outright rather than partly honoured.
func TestRerankerCannotResurrectADroppedResult(t *testing.T) {
	spy := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		return []provider.RankedPassage{{Text: passages[0], Score: 1}}
	}}
	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if out.Applied {
		t.Error("a truncated reply was applied")
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a truncated reply lost results: got %v, want %v", got, want)
	}
}

// TestRerankerDuplicateReplyRejected covers the third shape of a
// contract-breaking reply: the same passage twice, which would duplicate
// one row and evict another.
func TestRerankerDuplicateReplyRejected(t *testing.T) {
	spy := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		return []provider.RankedPassage{
			{Text: passages[0], Score: 3},
			{Text: passages[0], Score: 2},
			{Text: passages[1], Score: 1},
		}
	}}
	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if out.Applied {
		t.Error("a duplicating reply was applied")
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want fusion order %v", got, want)
	}
}

// TestRerankerNonFiniteScoreRejected: a NaN score makes the ordering
// comparison non-total, so the reply is refused rather than sorted into
// an arbitrary shape.
func TestRerankerNonFiniteScoreRejected(t *testing.T) {
	spy := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		out := make([]provider.RankedPassage, 0, len(passages))
		for _, p := range passages {
			out = append(out, provider.RankedPassage{Text: p, Score: math.NaN()})
		}
		return out
	}}
	out, err := Rerank(context.Background(), "q", fusedFixture(), enabled(spy))
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if out.Applied {
		t.Error("a NaN-scored reply was applied")
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want fusion order %v", got, want)
	}
}

// TestRerankerIdenticalTextAtTwoChunks: identical content at two paths
// shares a body but not a chunk row. One reply entry must consume one
// input slot, so neither row is duplicated nor dropped.
func TestRerankerIdenticalTextAtTwoChunks(t *testing.T) {
	fused := []FusedResult{
		{ChunkID: "a", Path: "/w/a.md", Trust: corpus.TrustTrusted},
		{ChunkID: "b", Path: "/w/b.md", Trust: corpus.TrustUntrustedSource},
	}
	same := func(FusedResult) string { return "identical body" }
	spy := &SpyReranker{reply: func(_ string, passages []string) []provider.RankedPassage {
		return []provider.RankedPassage{
			{Text: passages[0], Score: 2},
			{Text: passages[1], Score: 1},
		}
	}}

	out, err := Rerank(context.Background(), "q", fused,
		RerankOptions{Enabled: true, Reranker: spy, PassageText: same})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !out.Applied {
		t.Fatalf("identical bodies must still rerank: %v", out.Degraded)
	}
	if got, want := chunkIDs(out.Results), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
