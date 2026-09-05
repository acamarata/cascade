package retrieval

// Purpose: the full-text retrieval LEG — the adapter that presents the
//   index to the query-time fusion under the leg name rrf.StrategyFTS,
//   with exactly the shape the recall surface's Leg interface declares
//   (internal/retrieval/recall.Leg, the same shape
//   fusion.VectorLeg.Query has). It is the half of this ticket the rest of
//   the epic was waiting on: the acceptance harness stood a hand-written
//   lexical double in this position because nothing filled it.
//   Split from fts5.go under the Art.10.3 300-line file cap.
// Inputs: a resolved scope filter, the caller's query text, and a result
//   cap.
// Outputs: one ranked list plus whether the leg ran, or a taxonomy error.
// Constraints: the filter is consulted TWICE and decides nothing here.
//   Its predicate narrows the index scan before any document is read, and
//   Resolve re-binds every returned id on the way back — the same
//   both-ends discipline fusion.VectorLeg applies to its driver's raw
//   response, for the same reason: an id with no authorized record has no
//   resolved classification, and unclassified content is withheld.
// SPORT: internal.retrieval.Leg/ADDED (P1-E06-W2-S10-T2).

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Leg is the query-time full-text leg.
type Leg struct {
	index Querier
}

// NewLeg builds the leg over index.
//
// A nil index is the unconfigured case, and it is reported as a leg that
// did not RUN rather than as a failure, exactly as the vector leg reports
// a missing embedder: an install whose full-text index has not been built
// yet is a supported state, and fusion over the remaining legs is the
// documented degradation. It is never turned into an empty result, which
// a caller cannot tell apart from an index that holds nothing.
func NewLeg(index Querier) *Leg {
	return &Leg{index: index}
}

// Query returns the leg's ranked candidates for text.
//
// The second return value reports whether the leg ran. A malformed query
// is an error, never a skip and never an empty list: a query string the
// parser refuses has not been answered, and returning nothing for it would
// look identical to a corpus with no match.
func (l *Leg) Query(
	ctx context.Context, filter *fusion.ScopeFilter, text string, topK int,
) (rrf.RankedList, bool, error) {
	if filter == nil {
		return rrf.RankedList{}, false, cascade.New(cascade.KindInvalidInput,
			"retrieval: nil scope filter")
	}
	if topK <= 0 {
		return rrf.RankedList{}, false, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: topK must be positive, got %d", topK)
	}
	if l.index == nil {
		return rrf.RankedList{}, false, nil
	}
	list := rrf.RankedList{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight}
	if filter.Empty() {
		return list, true, nil
	}
	hits, err := l.index.Search(ctx, SearchRequest{Text: text, Scope: filter.Predicate()}, topK)
	if err != nil {
		return rrf.RankedList{}, false, err
	}
	list.Hits = resolveHits(filter, hits)
	return list, true, nil
}

// resolveHits re-binds each hit to the authorized record the scope filter
// already resolved, dropping any id the filter does not know.
//
// The index was handed the same filter's predicate and cannot return an
// unauthorized id, so this drop should never fire. It runs anyway, on the
// raw result and before anything is ranked, because "should never fire" is
// the property worth having a second, cheap enforcement of when what it
// protects is another session's content.
func resolveHits(filter *fusion.ScopeFilter, hits []Hit) []rrf.Candidate {
	out := make([]rrf.Candidate, 0, len(hits))
	for _, h := range hits {
		record, ok := filter.Resolve(h.ChunkID)
		if !ok {
			continue
		}
		out = append(out, rrf.Candidate{
			ChunkID:  record.ID,
			Path:     h.Path,
			CorpusID: record.CorpusID,
			Trust:    record.Trust,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
