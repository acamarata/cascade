// Purpose: Fuse, the Reciprocal Rank Fusion merge, plus the min-max
// normalization applied over the fused set, and the result-level dedupe
// that collapses a chunk several legs returned into one result.
//
// Inputs: the legs' ranked lists and the smoothing constant k.
// Outputs: results ordered by descending fused score, or a taxonomy error.
//
// Constraints: the output is a function of the input VALUES, never of the
// order the legs were passed in or of any map's iteration order. Two
// properties do that work together: every per-chunk contribution is summed
// in a canonical order (by leg name, then rank), so floating-point
// addition cannot reassociate differently between runs, and the final sort
// breaks score ties on the chunk id, so no two results can ever be
// interchangeable.
//
// SPORT: internal.retrieval.rrf.Fuse/ADDED (P1-E06-W2-S11-T1).

package rrf

import (
	"math"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// contribution is one leg's input to one chunk's score.
type contribution struct {
	strategy StrategyName
	rank     int
	weight   float64
}

// accumulator gathers everything the legs said about one chunk before any
// of it is combined, so the combination itself can be done in a canonical
// order rather than in arrival order.
type accumulator struct {
	chunkID       string
	paths         []string
	corpora       []string
	trusts        []corpus.TrustLevel
	contributions []contribution
}

// Fuse merges lists into one ranked answer.
//
// The score of chunk d is sum_i(weight_i / (k + rank_i(d))) over the legs
// that returned it, where rank is 1-based. A leg that did not return d
// contributes nothing for d, which is what lets a chunk found by one
// strong leg still place against a chunk found weakly by several.
//
// Results are ordered by descending fused score, ties broken by ascending
// chunk id. The tie-break is not cosmetic: RRF produces exact ties
// routinely (any two chunks holding mirrored ranks across two equally
// weighted legs tie exactly), and without a stable rule their order would
// come from map iteration and differ between runs of the same query.
//
// A nil lists is a caller bug and is refused. An empty lists, or lists
// whose legs all matched nothing, is a query that found nothing: it
// returns an empty result set and no error.
//
// Fuse is where the ranking ends unless the optional reranker stage is
// switched on. That stage is Rerank (reranker.go), which a caller runs
// over this function's output; with retrieval.reranker.enabled false —
// the default — it returns what Fuse returned, untouched and unread, so
// the two orderings cannot diverge.
func Fuse(lists []RankedList, k int64) ([]FusedResult, error) {
	if lists == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "rrf: nil ranked-list input")
	}
	if k <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"rrf: k must be positive, got %d", k)
	}
	if err := validateLists(lists); err != nil {
		return nil, err
	}
	accs, order := accumulate(lists)
	out := make([]FusedResult, 0, len(order))
	for _, id := range order {
		out = append(out, accs[id].fuse(k))
	}
	sortResults(out)
	applyNormalization(out)
	return out, nil
}

// validateLists refuses input a leg could not honestly have produced: a
// nameless or repeated leg, a weight that is not a usable number, a hit
// with no chunk id, or the same chunk returned twice by one leg (its rank
// would be ambiguous, and picking one silently would make the score depend
// on which was picked).
func validateLists(lists []RankedList) error {
	seenLeg := make(map[StrategyName]bool, len(lists))
	for _, l := range lists {
		if l.Strategy == "" {
			return cascade.New(cascade.KindInvalidInput, "rrf: ranked list has no strategy name")
		}
		if seenLeg[l.Strategy] {
			return cascade.Newf(cascade.KindInvalidInput,
				"rrf: strategy %q appears twice in one fusion", string(l.Strategy))
		}
		seenLeg[l.Strategy] = true
		if math.IsNaN(l.Weight) || math.IsInf(l.Weight, 0) || l.Weight < 0 {
			return cascade.Newf(cascade.KindInvalidInput,
				"rrf: strategy %q has weight %v, which is not a usable weight", string(l.Strategy), l.Weight)
		}
		if err := validateHits(l); err != nil {
			return err
		}
	}
	return nil
}

// validateHits checks one leg's hits.
func validateHits(l RankedList) error {
	seen := make(map[string]bool, len(l.Hits))
	for i, h := range l.Hits {
		if h.ChunkID == "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"rrf: strategy %q returned a hit with no chunk id at rank %d", string(l.Strategy), i+1)
		}
		if seen[h.ChunkID] {
			return cascade.Newf(cascade.KindInvalidInput,
				"rrf: strategy %q returned chunk %s twice", string(l.Strategy), h.ChunkID)
		}
		seen[h.ChunkID] = true
	}
	return nil
}

// accumulate gathers every leg's statement about every chunk. order is
// first-appearance order and exists only to give the loop in Fuse a
// deterministic walk; it never decides the ranking, which the sort does.
func accumulate(lists []RankedList) (map[string]*accumulator, []string) {
	accs := make(map[string]*accumulator)
	var order []string
	for _, l := range lists {
		for i, h := range l.Hits {
			a, ok := accs[h.ChunkID]
			if !ok {
				a = &accumulator{chunkID: h.ChunkID}
				accs[h.ChunkID] = a
				order = append(order, h.ChunkID)
			}
			a.observe(l, h, i+1)
		}
	}
	return accs, order
}

// observe records one hit against its chunk.
func (a *accumulator) observe(l RankedList, h Candidate, rank int) {
	a.contributions = append(a.contributions, contribution{
		strategy: l.Strategy,
		rank:     rank,
		weight:   l.Weight,
	})
	if h.Path != "" {
		a.paths = append(a.paths, h.Path)
	}
	if h.CorpusID != "" {
		a.corpora = append(a.corpora, h.CorpusID)
	}
	a.trusts = append(a.trusts, h.Trust)
}

// fuse turns one chunk's accumulated observations into its result.
//
// Contributions are sorted before they are summed. Floating-point addition
// is not associative, so summing them in arrival order would let the same
// query produce a score differing in its last bits when the legs were
// passed in a different order, and a difference in the last bits is enough
// to flip an otherwise exact tie.
func (a *accumulator) fuse(k int64) FusedResult {
	sort.Slice(a.contributions, func(i, j int) bool {
		if a.contributions[i].strategy != a.contributions[j].strategy {
			return a.contributions[i].strategy < a.contributions[j].strategy
		}
		return a.contributions[i].rank < a.contributions[j].rank
	})
	var raw float64
	strategies := make([]StrategyName, 0, len(a.contributions))
	for _, c := range a.contributions {
		raw += c.weight / float64(k+int64(c.rank))
		if len(strategies) == 0 || strategies[len(strategies)-1] != c.strategy {
			strategies = append(strategies, c.strategy)
		}
	}
	return FusedResult{
		ChunkID:    a.chunkID,
		Path:       smallest(a.paths),
		CorpusID:   smallest(a.corpora),
		Trust:      agreedTrust(a.trusts),
		RawScore:   raw,
		Strategies: strategies,
	}
}

// smallest returns the lexicographically first of the observed values, or
// the empty string when nothing was observed. Identical content at two
// paths shares one chunk id by design, so more than one value here is
// expected rather than exceptional; picking the first-seen one would make
// the field depend on leg order, and picking the smallest does not.
func smallest(values []string) string {
	out := ""
	for _, v := range values {
		if out == "" || v < out {
			out = v
		}
	}
	return out
}

// agreedTrust returns the TRUST tag every leg agreed on, or
// corpus.TrustUntrustedSource when they did not agree or when any observed
// tag is not a defined level.
//
// The legs read one scope-filtered candidate set, so disagreement means
// the same content was reached through two differently classified corpora.
// Resolving that to the untrusted side is the corpus model's own
// fail-closed rule for a tag that does not resolve, applied here rather
// than restated: a value that cannot be read must not become a permission.
func agreedTrust(observed []corpus.TrustLevel) corpus.TrustLevel {
	if len(observed) == 0 {
		return corpus.TrustUntrustedSource
	}
	first := observed[0]
	if !first.Valid() {
		return corpus.TrustUntrustedSource
	}
	for _, t := range observed[1:] {
		if t != first {
			return corpus.TrustUntrustedSource
		}
	}
	return first
}

// sortResults orders the fused set: descending score, then ascending chunk
// id. Chunk ids are unique after the dedupe, so the comparison is a total
// order and the result is one specific permutation, not one of several.
func sortResults(out []FusedResult) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].RawScore != out[j].RawScore {
			return out[i].RawScore > out[j].RawScore
		}
		return out[i].ChunkID < out[j].ChunkID
	})
}

// applyNormalization writes the normalized score onto each result.
func applyNormalization(out []FusedResult) {
	raw := make([]float64, len(out))
	for i := range out {
		raw[i] = out[i].RawScore
	}
	norm := normalizeScores(raw)
	for i := range out {
		out[i].Score = norm[i]
	}
}

// normalizeScores min-max normalizes scores into [0,1], preserving their
// order.
//
// The degenerate cases are the point of this function existing separately.
// An empty input returns an empty result. A single score, and a set whose
// scores are all equal, both normalize to 1.0 rather than to 0.0: the
// formula is undefined there, and 0.0 would report the best available
// evidence as no evidence at all. Negative scores are handled by the same
// subtraction as any other, so the function does not depend on RRF's own
// scores happening to be positive.
func normalizeScores(scores []float64) []float64 {
	out := make([]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	minScore, maxScore := scores[0], scores[0]
	for _, s := range scores[1:] {
		minScore = math.Min(minScore, s)
		maxScore = math.Max(maxScore, s)
	}
	span := maxScore - minScore
	if span <= 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}
	for i, s := range scores {
		out[i] = math.Min(1.0, math.Max(0.0, (s-minScore)/span))
	}
	return out
}
