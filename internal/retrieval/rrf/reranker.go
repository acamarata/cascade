// Purpose: the optional post-fusion reranker stage — the seam a
// cross-encoder plugs into to reorder the fused candidate set against the
// query, gated by the retrieval.reranker.enabled config key (default
// false).
//
// Inputs: the fused results, the query, and RerankOptions carrying the
// enabled flag, the registered provider.Reranker, and the resolver that
// supplies each result's passage text.
//
// Outputs: a RerankOutcome whose Results are always a permutation of the
// fused input — reordered when the reranker succeeded, in fusion order
// when it did not — plus a typed config error when the stage is enabled
// but unusable.
//
// Constraints: this stage REORDERS and does nothing else. It never adds a
// result, never removes one, never edits one, and never re-reads the
// corpus, so it cannot widen what scope resolution already withheld: the
// output is built by permuting the input slice's own values, and a
// reranker reply that is not a permutation of what it was handed is
// refused outright. The reranker is third-party output and is treated as
// untrusted throughout. No I/O and no clock of its own: the deadline and
// cancellation ride on ctx (06 §2 — no bare time.Now).
//
// SPORT: internal.retrieval.rrf.Rerank/ADDED (P1-E06-W2-S12-T3).

package rrf

import (
	"context"
	"math"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RerankOptions is the stage's whole configuration.
//
// Enabled mirrors the retrieval.reranker.enabled config key
// (08-INIT-CONFIG-SPEC §3, TOML [retrieval.reranker] enabled = false).
// The two remaining fields are what the stage needs to be able to run at
// all, and when Enabled is true, both are required: an enabled stage with
// nothing to run is a misconfiguration, not a no-op (see Rerank).
type RerankOptions struct {
	// Enabled gates the whole stage. False is the default and the
	// shipped behaviour: Rerank returns the fused results untouched.
	Enabled bool
	// Reranker is the registered implementation. Nil while Enabled is
	// false is the ordinary case; nil while Enabled is true is the
	// fail-closed error.
	Reranker provider.Reranker
	// PassageText resolves the text a result's chunk holds, which is the
	// only thing a Reranker sees. It is injected rather than read from
	// FusedResult because a fused result carries an identity and a
	// ranking, not a body: the store that can produce the body belongs to
	// the caller, not to this package.
	PassageText func(FusedResult) string
}

// RerankOutcome is the stage's report. Results is always usable: on every
// path that does not return an error, it holds every fused result exactly
// once.
type RerankOutcome struct {
	// Results are the fused results, reordered by the reranker when
	// Applied is true and in the original fusion order when it is not.
	Results []FusedResult
	// Applied reports whether the reranker's ordering was actually used.
	Applied bool
	// Degraded is the reason the reranker's ordering was NOT used, on the
	// paths where the stage ran and fell back: a reranker error, a
	// timeout, a cancellation, or a reply that broke the Reranker
	// contract. It is nil when Applied is true and when the stage was
	// disabled. It exists so that falling back is reported rather than
	// silent — a caller that ignores it has chosen to, and a caller that
	// logs it can tell a degraded query from a healthy one.
	Degraded error
}

// Rerank runs the optional reranker stage over fused.
//
// DISABLED IS EXACTLY DISABLED. With Enabled false, Rerank returns the
// caller's own slice, unread and unexamined, and reports Applied false.
// It performs no validation, so a disabled stage cannot introduce a
// failure mode that fusion alone did not have, and the result is
// identical to not calling Rerank at all. That is the contract the
// default configuration ships under.
//
// FAIL-CLOSED ON CONFIGURATION. With Enabled true and either no Reranker
// registered or no PassageText resolver, Rerank returns a typed
// KindInvalidInput error and no results. It never quietly degrades to
// fusion order: a user who configured a reranker and silently got none
// has been misled about what ran, which is the failure mode
// §5.20 exists to prevent. Configuration is decided before the query, so
// it is the caller's bug to fix, not a runtime condition to absorb.
//
// FALL BACK, NEVER DROP, ON RUNTIME FAILURE. Once the stage is properly
// configured, no reranker behaviour can cost the caller a result. A
// reranker that errors, that exceeds ctx's deadline, that is canceled, or
// that returns a reply violating provider.Reranker's completeness
// contract all land on the same path: the complete fused set in fusion
// order, Applied false, and the cause in Degraded. This is a deliberate
// choice between the two defensible policies. Failing the query would
// throw away a complete, correct, already scope-checked answer because an
// optional quality stage misbehaved; fusion order is precisely the answer
// the caller would have received with the stage switched off. What is
// never acceptable — and is what Degraded rules out — is returning fewer
// results, or returning fusion order while implying the reranker ran.
//
// THE RERANKER IS UNTRUSTED. Its reply is checked against
// provider.ValidRanking and then mapped back onto the caller's own
// FusedResult values by text, one input slot consumed per returned
// passage. A reply carrying an extra passage, a duplicate, an edited
// string, a missing candidate, or a non-finite score is rejected in full
// rather than partially honoured, so nothing the reranker invents can
// reach the output and nothing scope withheld can be resurrected through
// it. Each surviving row is the fused row itself: its trust tag, corpus,
// path and fused scores travel with it, so reordering cannot launder a
// row's provenance into another's.
//
// DETERMINISM. The reranker's scores decide the order; exact ties break
// on ascending chunk id, the same rule fusion itself uses, so a
// deterministic reranker produces byte-identical output for identical
// input regardless of the order it happened to list its ties in.
func Rerank(ctx context.Context, query string, fused []FusedResult, opts RerankOptions) (RerankOutcome, error) {
	if !opts.Enabled {
		return RerankOutcome{Results: fused}, nil
	}
	if err := opts.validate(fused); err != nil {
		return RerankOutcome{}, err
	}
	passages := make([]string, len(fused))
	for i, r := range fused {
		passages[i] = opts.PassageText(r)
	}
	ranked, err := opts.Reranker.Rerank(ctx, query, passages)
	if err != nil {
		return RerankOutcome{Results: fused, Degraded: err}, nil
	}
	ordered, err := reorder(fused, passages, ranked)
	if err != nil {
		return RerankOutcome{Results: fused, Degraded: err}, nil
	}
	return RerankOutcome{Results: ordered, Applied: true}, nil
}

// validate refuses an enabled stage that cannot honestly run: a missing
// implementation, a missing text resolver, or a candidate set whose chunk
// ids are not unique (which would leave the tie-break rule unable to
// impose a total order, so "deterministic" would stop being true). Fuse
// dedupes on chunk id, so the last case only arises from a hand-built
// candidate set.
func (o RerankOptions) validate(fused []FusedResult) error {
	if o.Reranker == nil {
		return cascade.New(cascade.KindInvalidInput,
			"rrf: retrieval.reranker.enabled is true but no reranker implementation is registered")
	}
	if o.PassageText == nil {
		return cascade.New(cascade.KindInvalidInput,
			"rrf: retrieval.reranker.enabled is true but no passage-text resolver was supplied")
	}
	seen := make(map[string]bool, len(fused))
	for _, r := range fused {
		if seen[r.ChunkID] {
			return cascade.Newf(cascade.KindInvalidInput,
				"rrf: reranker input contains chunk %s twice", r.ChunkID)
		}
		seen[r.ChunkID] = true
	}
	return nil
}

// rerankRow pairs one fused result with the score the reranker gave it.
// The fused result is carried by value and never rewritten: the reranker
// score orders the rows and is then discarded, because it is on a
// model-defined scale that is not comparable with the normalized fusion
// score the result already carries.
type rerankRow struct {
	result FusedResult
	score  float64
}

// reorder maps a reranker reply back onto the fused results, or refuses
// it. The mapping is by text and consumes one input slot per returned
// passage, so identical text at two chunks is handled without either
// chunk being duplicated or dropped.
func reorder(fused []FusedResult, passages []string, ranked []provider.RankedPassage) ([]FusedResult, error) {
	if !provider.ValidRanking(passages, ranked) {
		return nil, cascade.New(cascade.KindIntegrity,
			"rrf: reranker reply is not a complete, ordered permutation of the candidates it was given")
	}
	slots := make(map[string][]int, len(passages))
	for i, p := range passages {
		slots[p] = append(slots[p], i)
	}
	rows := make([]rerankRow, 0, len(ranked))
	for _, rp := range ranked {
		if math.IsNaN(rp.Score) || math.IsInf(rp.Score, 0) {
			return nil, cascade.New(cascade.KindIntegrity,
				"rrf: reranker returned a score that is not a usable number")
		}
		idx := slots[rp.Text]
		if len(idx) == 0 {
			return nil, cascade.New(cascade.KindIntegrity,
				"rrf: reranker returned a passage that was not among the candidates")
		}
		slots[rp.Text] = idx[1:]
		rows = append(rows, rerankRow{result: fused[idx[0]], score: rp.Score})
	}
	return sortRerankRows(rows), nil
}

// sortRerankRows orders by descending reranker score, then by ascending
// chunk id — the same tie-break sortResults applies to the fused set, so
// the two stages cannot disagree about what "a stable order" means. Chunk
// ids are unique (validate checked), so the comparison is a total order
// and the output is one specific permutation.
func sortRerankRows(rows []rerankRow) []FusedResult {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return rows[i].result.ChunkID < rows[j].result.ChunkID
	})
	out := make([]FusedResult, len(rows))
	for i, r := range rows {
		out[i] = r.result
	}
	return out
}
