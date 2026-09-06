package eval

// Purpose: the three measurements R-16.9's gate is built from: recall@10
//   itself, the relative lift the RRF leg gives the FTS5-only leg, and the
//   no-known-item-loss property.
// Inputs: a QuerySet's queries paired with the harness's RankedResult for
//   each, in the same order.
// Outputs: a float64 metric, or a *cascade.Error when the inputs cannot
//   honestly produce one (a zero denominator, a result-count mismatch).
// Constraints: rank 10 counts, rank 11 does not (the RankedIDs slice is
//   capped here, never trusted to already be capped by the caller); a
//   zero FTS5 semantic denominator is a typed failure, never a permissive
//   default.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// topN is the recall@10 rank boundary: rank 10 is included, rank 11 is
// excluded.
const topN = 10

// recallEpsilon absorbs floating-point noise when comparing two
// independently averaged recall values; a drop smaller than this is not a
// real regression.
const recallEpsilon = 1e-9

// RecallAt10 computes recall@10 across results: for each query, the
// fraction of its expected ids found within the first 10 ranked ids,
// averaged over every query. results must pair positionally with queries
// (the harness produces them in the query set's order).
func RecallAt10(queries []Query, results []RankedResult) (float64, error) {
	if len(queries) == 0 {
		return 0, cascade.New(cascade.KindInvalidInput, "eval: recall@10: empty query set")
	}
	if len(results) != len(queries) {
		return 0, cascade.Newf(cascade.KindInvalidInput,
			"eval: recall@10: %d results for %d queries", len(results), len(queries))
	}
	var total float64
	for i, q := range queries {
		total += perQueryRecall(q, results[i])
	}
	return total / float64(len(queries)), nil
}

// perQueryRecall is the fraction of q's expected ids present in r's top
// 10 ranked ids.
func perQueryRecall(q Query, r RankedResult) float64 {
	top := capTop(r.RankedIDs)
	found := 0
	for _, id := range q.ExpectedIDs {
		if containsID(top, id) {
			found++
		}
	}
	return float64(found) / float64(len(q.ExpectedIDs))
}

// capTop returns ids capped to the top 10; rank 11 onward never counts.
func capTop(ids []string) []string {
	if len(ids) > topN {
		return ids[:topN]
	}
	return ids
}

// containsID reports whether id appears in ids.
func containsID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

// RelativeRecallLift computes (rrfRecall - fts5Recall) / fts5Recall. A
// zero fts5Recall denominator is a typed gate failure: dividing by zero
// would either panic (integer) or silently produce +Inf (float), and
// either would let a corpus with no FTS5 semantic signal at all pass the
// gate by construction.
func RelativeRecallLift(fts5Recall, rrfRecall float64) (float64, error) {
	if fts5Recall == 0 {
		return 0, cascade.New(cascade.KindInvalidInput,
			"eval: relative recall lift: fts5-only semantic recall@10 is zero")
	}
	return (rrfRecall - fts5Recall) / fts5Recall, nil
}

// NoKnownItemLoss requires every expected id the FTS5-only leg found in
// its own top 10 to remain in the RRF-fused top 10, per query, and
// requires aggregate RRF known-item recall@10 to be no lower than
// FTS5-only's. ok is false the moment either fails; detail names the
// first failure.
func NoKnownItemLoss(queries []Query, fts5Results, fusedResults []RankedResult) (ok bool, detail string, err error) {
	if len(fts5Results) != len(queries) || len(fusedResults) != len(queries) {
		return false, "", cascade.New(cascade.KindInvalidInput,
			"eval: known-item loss check: result count does not match query count")
	}
	for i, q := range queries {
		if lost := lostItem(q, fts5Results[i], fusedResults[i]); lost != "" {
			return false, fmt.Sprintf("query %q: fts5 found %q in its top 10 but rrf fusion dropped it", q.Text, lost), nil
		}
	}
	fts5Recall, err := RecallAt10(queries, fts5Results)
	if err != nil {
		return false, "", err
	}
	fusedRecall, err := RecallAt10(queries, fusedResults)
	if err != nil {
		return false, "", err
	}
	if fusedRecall < fts5Recall-recallEpsilon {
		return false, fmt.Sprintf("aggregate rrf known-item recall@10 %.4f is below fts5-only %.4f", fusedRecall, fts5Recall), nil
	}
	return true, "", nil
}

// lostItem returns the first expected id fts5 found in its top 10 that
// fused's top 10 no longer holds, or "" when none was lost.
func lostItem(q Query, fts5, fused RankedResult) string {
	fts5Top := capTop(fts5.RankedIDs)
	fusedTop := capTop(fused.RankedIDs)
	for _, id := range q.ExpectedIDs {
		if containsID(fts5Top, id) && !containsID(fusedTop, id) {
			return id
		}
	}
	return ""
}
