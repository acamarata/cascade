// Purpose: declare the Reranker family contract: the dep-free seam an
//   optional cross-encoder stage implements to reorder candidate passages
//   against a query after first-pass retrieval.
// Inputs: a context, a query string, and the candidate passage texts.
// Outputs: the same passages carrying relevance scores, ordered best first,
//   or a pkg/cascade taxonomy error.
// Constraints: pkg/provider imports only stdlib (Art.10.2): no internal/,
//   no third-party, no CGO; this file declares contracts only, every
//   implementation ships elsewhere (Art.1).
// SPORT: pkg.provider.Reranker/ADDED (P1-E06-W2-S10-T5).

package provider

import "context"

// Reranker reorders candidate passages by relevance to a query. It is the
// optional second stage of retrieval: a cheap first pass (vector search,
// full-text search, or their fusion) proposes candidates, and a Reranker
// scores them with a model that sees the query and the passage together.
//
// SDK-INTENT: this is a contract only. No implementation and no caller ship
// with it yet: reranking is an optional retrieval stage that lands in a
// later ticket, and third-party providers implement it from outside this
// module.
//
// A Reranker is not a retriever. It never introduces a passage the caller
// did not supply, and it never removes one: truncating to a top-N is the
// caller's decision, taken after seeing the scores.
type Reranker interface {
	// Rerank scores each passage against query and returns them ordered
	// best first.
	//
	// Contract, binding on every implementation:
	//
	//   - Completeness: the result contains every element of passages
	//     exactly once. A passage is neither dropped, duplicated, edited,
	//     nor invented; RankedPassage.Text is the input string verbatim.
	//     Duplicate input passages produce that many result entries.
	//   - Ordering: results are sorted by Score descending. Ties may be
	//     broken in any order, so a caller that needs stability must
	//     impose it.
	//   - All-or-nothing: Rerank returns either a complete ranking and a
	//     nil error, or a nil slice and a non-nil error. There is no
	//     partial ranking.
	//   - Empty input: Rerank with nil or empty passages returns an empty
	//     slice and a nil error, whatever the query. An empty candidate
	//     set is the ordinary result of a first pass that matched
	//     nothing, not an error.
	//   - Cancellation: Rerank honors ctx and returns a
	//     cascade.KindCanceled or cascade.KindTimeout error rather than a
	//     partial ranking.
	//
	// Errors returned at this boundary are pkg/cascade taxonomy errors:
	// KindInvalidInput for a query or passage the backend rejects,
	// KindUnavailable or KindTimeout for an unreachable backend,
	// KindQuotaExhausted when a provider quota is spent, KindCanceled for
	// a canceled context.
	Rerank(ctx context.Context, query string, passages []string) ([]RankedPassage, error)
}

// RankedPassage is one scored candidate returned by Reranker.Rerank.
type RankedPassage struct {
	// Text is the candidate passage, verbatim as it was passed to Rerank.
	Text string
	// Score is the relevance of Text to the query; higher is more
	// relevant. The scale is model-defined and is not comparable across
	// rerankers, across queries, or against a VectorMatch similarity:
	// scores are meaningful only as an ordering within one Rerank result.
	Score float64
}

// ValidRanking reports whether ranked is a well-formed Rerank result for
// passages. It is the structural half of the Rerank contract, checkable by a
// caller that does not trust a third-party implementation: the same passages
// come back, each exactly as many times as it went in, in non-increasing
// score order.
//
// It does not judge the scores themselves. A reranker that returns a
// constant score for every passage produces a valid ranking and a useless
// one, which is a quality question rather than a contract violation.
func ValidRanking(passages []string, ranked []RankedPassage) bool {
	if len(ranked) != len(passages) {
		return false
	}
	remaining := make(map[string]int, len(passages))
	for _, p := range passages {
		remaining[p]++
	}
	for i, r := range ranked {
		if remaining[r.Text] == 0 {
			return false
		}
		remaining[r.Text]--
		if i > 0 && ranked[i-1].Score < r.Score {
			return false
		}
	}
	return true
}
