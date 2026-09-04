// Purpose: the recall query's own steps, split from service.go under the
// 300-line file cap: the last-moment re-authorization of every fused row,
// the shaping of the answer, and the validation of a request's bounds and
// scope inputs. Same concern as service.go, not a different one.
//
// Inputs: a Request's raw fields, and the fused rows a query produced.
// Outputs: a Response, or a pkg/cascade taxonomy error.
//
// Constraints: no I/O here — every function is a pure function of its
// arguments and of the scope filter it consults.
//
// SPORT: internal.retrieval.recall.Service/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// authorize re-resolves every fused row against the filter that produced
// the candidate set, dropping any row the filter does not authorize and
// counting it instead.
//
// This is not a second authorization decision: it is the same decision,
// consulted again at the last moment before a row becomes output. The
// citation assembler does exactly this for the same reason; doing it for
// the results too means a leg that returned an id it was never given
// cannot put that id in front of a user through the results half of the
// answer while the citations half correctly withholds it.
func authorize(fused []rrf.FusedResult, filter *fusion.ScopeFilter) ([]rrf.FusedResult, int) {
	out := make([]rrf.FusedResult, 0, len(fused))
	withheld := 0
	for _, r := range fused {
		if _, ok := filter.Resolve(r.ChunkID); !ok {
			withheld++
			continue
		}
		out = append(out, r)
	}
	return out, withheld
}

// response assembles the answer value from the authorized rows.
func response(
	query string, fused []rrf.FusedResult, set citations.CitationSet, withheld int, ran []string,
) Response {
	out := Response{
		Query:     query,
		Results:   make([]Result, 0, len(fused)),
		Citations: set.Citations,
		Withheld:  withheld + set.Withheld,
		Legs:      ran,
	}
	if out.Citations == nil {
		out.Citations = []citations.Citation{}
	}
	for i, r := range fused {
		out.Results = append(out.Results, Result{
			Rank: i + 1, ChunkID: r.ChunkID, Path: r.Path, CorpusID: r.CorpusID,
			Trust: r.Trust, Score: r.Score, RawScore: r.RawScore, Strategies: r.Strategies,
		})
	}
	out.Rendered = citations.Render(set).Definitions
	return out
}

// effectiveK validates and resolves the request's result cap.
func effectiveK(req Request) (int, error) {
	if strings.TrimSpace(req.Query) == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "recall: the query is empty")
	}
	switch {
	case req.K < 0:
		return 0, cascade.Newf(cascade.KindInvalidInput, "recall: k must not be negative, got %d", req.K)
	case req.K == 0:
		return DefaultK, nil
	case req.K > MaxK:
		return 0, cascade.Newf(cascade.KindInvalidInput,
			"recall: k must be at most %d, got %d", MaxK, req.K)
	}
	return req.K, nil
}

// corpusQuery turns the request's scope inputs into the corpus model's own
// query value, refusing a malformed one rather than narrowing it silently.
func corpusQuery(req Request) (corpus.Query, error) {
	entitlement := corpus.PrivacyProject
	if req.Entitlement != "" {
		entitlement = corpus.PrivacyClass(req.Entitlement)
		if !entitlement.Valid() {
			return corpus.Query{}, cascade.Newf(cascade.KindInvalidInput,
				"recall: %q is not a privacy tier", req.Entitlement)
		}
	}
	q := corpus.Query{
		Membership:  corpus.Membership{Scope: corpus.ScopeRef(req.Scope)},
		Entitlement: entitlement,
		CorpusIDs:   append([]string(nil), req.Corpora...),
	}
	if err := q.Membership.Validate(); err != nil {
		return corpus.Query{}, err
	}
	return q, nil
}

// checkCorpora refuses a --corpus name that resolved to nothing this
// session may read.
//
// The refusal is deliberately identical for a corpus that does not exist,
// a corpus this session may not see, and a corpus that holds no indexed
// record: a message that distinguished them would disclose the existence
// of content the caller is not entitled to know about, and the caller's
// next step — check the name, check the index — is the same in all three
// cases.
func checkCorpora(requested []string, filter *fusion.ScopeFilter) error {
	if len(requested) == 0 {
		return nil
	}
	available := make(map[string]bool)
	for _, id := range filter.Predicate().CorpusIDs {
		available[id] = true
	}
	for _, id := range requested {
		if !available[id] {
			return cascade.Newf(cascade.KindNotFound,
				"recall: no readable corpus named %q is in this index", id)
		}
	}
	return nil
}
