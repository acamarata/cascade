// Package recall is the query surface of retrieval: the one place the
// pieces below it — the corpus catalog, the scope filter, the retrieval
// legs, RRF fusion and the citation assembler — are composed into a single
// answer for a caller who asked a question.
//
// Purpose: hold the composition, and hold the guarantee that composition
// is where a scope leak would appear. Every part below enforces scope
// separately; this package is the seam, so it re-asks the scope filter
// about every result it is about to describe and counts, never names, the
// ones it will not describe.
//
// Inputs: a Request (the query text, the asking session's scope and
// entitlement, the corpora it wants narrowed to, and how many results it
// wants), resolved against an injected Catalog and the injected legs.
//
// Outputs: a Response carrying the ranked results, their citations, and
// the count of results withheld — or a pkg/cascade taxonomy error.
//
// Constraints: nothing here decides authorization. The corpus model takes
// that decision, the scope filter carries it, and this package consults
// it. An empty answer is never manufactured from a failure: a query that
// found nothing returns an empty Response and no error, while a broken
// index, an unknown corpus and a malformed query each return a typed
// error, because a caller cannot tell those apart from an empty list.
//
// SPORT: internal.retrieval.recall.Service/ADDED (P1-E06-W2-S11-T3).
package recall

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultK is the result cap a request that names none is answered with.
// A recall with no bound would page a whole index into a terminal.
const DefaultK = 10

// MaxK bounds what a caller may ask for. It is a bound on this surface's
// own work, not a policy decision: a peer that asks for ten million
// results should be refused rather than served slowly.
const MaxK = 500

// Catalog resolves the retrieval index's corpus model for one query.
//
// It is an interface because the catalog's storage is not this package's
// business: FileCatalog (catalog.go) is the shipped implementation, and a
// future index built by the index-lifecycle verbs replaces it without
// this file changing. Load returns a taxonomy error rather than an empty
// Index for an index that could not be read — the distinction between
// "nothing matched" and "your index is broken" is the whole reason this
// returns an error at all.
type Catalog interface {
	// Load returns the index's corpus model.
	Load(ctx context.Context) (*Index, error)
}

// Index is one loaded catalog: the corpus model plus the legs' view of it.
type Index struct {
	// Store is the corpus model every authorization decision is taken
	// against. Required.
	Store *corpus.Store
}

// Leg is one retrieval leg, exactly as the query-time fusion path declares
// it (internal/retrieval/fusion.VectorLeg.Query's signature). Declaring
// the shape rather than the type means any leg that can rank a
// scope-filtered candidate set is usable here, and that this package
// holds no list of which legs exist.
//
// The bool result reports whether the leg RAN. False with a nil error is
// a leg that is not configured in this build, which is a degradation, not
// a failure.
type Leg interface {
	Query(ctx context.Context, filter *fusion.ScopeFilter, text string, topK int) (rrf.RankedList, bool, error)
}

// Request is one recall query.
type Request struct {
	// Query is the search text. Required: an empty query is refused
	// rather than answered with everything or with nothing.
	Query string
	// Corpora optionally narrows the query to named corpora. A name the
	// session is not authorized to read is refused exactly as a name
	// that does not exist is, so the refusal discloses nothing.
	Corpora []string
	// Scope is the asking session's own scope reference. Required.
	Scope string
	// Entitlement is the highest privacy tier this query may see. Empty
	// resolves to the project tier, which is the fail-closed default:
	// personal content is served only to a caller that stated personal
	// entitlement.
	Entitlement string
	// K caps the results. Zero uses DefaultK.
	K int
}

// Result is one ranked answer row.
type Result struct {
	Rank       int                `json:"rank"`
	ChunkID    string             `json:"chunk_id"`
	Path       string             `json:"path,omitempty"`
	CorpusID   string             `json:"corpus_id,omitempty"`
	Trust      corpus.TrustLevel  `json:"trust"`
	Score      float64            `json:"score"`
	RawScore   float64            `json:"raw_score"`
	Strategies []rrf.StrategyName `json:"strategies,omitempty"`
}

// Response is one recall answer.
type Response struct {
	// Query echoes the text that was asked, so a stored or piped answer
	// still says what question it answers.
	Query string `json:"query"`
	// Results are the ranked rows, best first, capped at the request's K.
	Results []Result `json:"results"`
	// Citations describe those rows. They always ride the response: a
	// result whose provenance is optional is a result a reader cannot
	// check.
	Citations []citations.Citation `json:"citations"`
	// Withheld counts rows the scope filter did not authorize on the way
	// out. It is a count and nothing else — no path, corpus or id of a
	// withheld row appears anywhere in this value.
	Withheld int `json:"withheld"`
	// Rendered is the Markdown footnote block for Citations.
	Rendered string `json:"rendered,omitempty"`
	// Legs names the retrieval legs that ran, so a degraded query (one
	// leg unavailable) is visible rather than looking like a thin index.
	Legs []string `json:"legs"`
}

// Service answers recall queries over a catalog and a set of legs.
type Service struct {
	catalog Catalog
	params  rrf.Params
	legs    []Leg
}

// NewService builds a Service. catalog is required; legs may be empty,
// which is a build with no retrieval leg configured — a state Query
// reports as unavailable rather than as an empty answer.
func NewService(catalog Catalog, params rrf.Params, legs ...Leg) (*Service, error) {
	if catalog == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "recall: no catalog")
	}
	return &Service{catalog: catalog, params: params, legs: legs}, nil
}

// Query runs one recall.
//
// The order is the order the guarantee needs: the catalog is read, the
// scope filter resolves the authorized candidate set from it, the legs
// run against that set and only that set, fusion ranks what they
// returned, and every ranked row is re-resolved against the same filter
// before it is described. Nothing is filtered after ranking that was not
// already narrowed before it; the re-resolution exists to catch a leg
// that returned a row it was never given.
func (s *Service) Query(ctx context.Context, req Request) (Response, error) {
	k, err := effectiveK(req)
	if err != nil {
		return Response{}, err
	}
	q, err := corpusQuery(req)
	if err != nil {
		return Response{}, err
	}
	index, err := s.catalog.Load(ctx)
	if err != nil {
		return Response{}, err
	}
	if index == nil || index.Store == nil {
		return Response{}, cascade.New(cascade.KindIntegrity,
			"recall: the retrieval index loaded without a corpus model")
	}
	filter, err := fusion.NewScopeFilter(index.Store, q)
	if err != nil {
		return Response{}, err
	}
	if err := checkCorpora(req.Corpora, filter); err != nil {
		return Response{}, err
	}
	return s.rank(ctx, req, filter, k)
}

// rank runs the legs, fuses their lists and describes the result.
func (s *Service) rank(
	ctx context.Context, req Request, filter *fusion.ScopeFilter, k int,
) (Response, error) {
	lists, ran, err := s.runLegs(ctx, filter, req.Query, k)
	if err != nil {
		return Response{}, err
	}
	if len(ran) == 0 {
		return Response{}, cascade.New(cascade.KindUnavailable,
			"recall: no retrieval leg is available; the index cannot be queried")
	}
	fused, err := rrf.FuseWith(lists, s.params)
	if err != nil {
		return Response{}, err
	}
	if len(fused) > k {
		fused = fused[:k]
	}
	authorized, withheld := authorize(fused, filter)
	set, err := citations.Assemble(authorized, citations.Options{Resolver: filter})
	if err != nil {
		return Response{}, err
	}
	return response(req.Query, authorized, set, withheld, ran), nil
}

// runLegs queries every configured leg, keeping the lists of those that
// ran. A leg that reports it did not run contributes nothing and is not
// an error: an install with no embedding provider is a supported
// configuration, and fusion over the remaining legs is the documented
// degradation.
func (s *Service) runLegs(
	ctx context.Context, filter *fusion.ScopeFilter, text string, k int,
) ([]rrf.RankedList, []string, error) {
	lists := make([]rrf.RankedList, 0, len(s.legs))
	var ran []string
	for _, leg := range s.legs {
		if leg == nil {
			continue
		}
		list, ok, err := leg.Query(ctx, filter, text, k)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		lists = append(lists, list)
		ran = append(ran, string(list.Strategy))
	}
	sort.Strings(ran)
	return lists, ran, nil
}
