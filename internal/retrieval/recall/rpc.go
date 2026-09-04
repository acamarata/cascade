// Purpose: the recall.* JSON-RPC namespace — the typed params the daemon
// decodes an untrusted peer's request into, the Handler that serves
// recall.query over the Service, and the Register call the daemon
// composition root makes so the namespace is reachable from a running
// daemon rather than merely built.
//
// Inputs: raw JSON params from an untrusted peer; a *Service.
// Outputs: a QueryResult marshalled into the JSON-RPC response, or a
// pkg/cascade taxonomy error carrying the Kind that classifies the
// refusal.
//
// Constraints: params decode into a concrete struct, never interface{};
// every refusal is a taxonomy error, never a bare string. The result
// always carries its citations array (v1 parity: a search response
// describes its own provenance), and never carries anything about a
// withheld row beyond the count the Service already reduced it to.
//
// SPORT: internal.retrieval.recall.Handler/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodQuery is the recall namespace's fused-query method. It is a
// constant because the daemon registers it and the CLI calls it by the
// same name; a literal typed twice is a namespace that half-exists.
const MethodQuery = "recall.query"

// MethodSearchAlias is the v1-parity alias for MethodQuery. v1 exposed
// this surface as `cascade_search`, and callers written against it keep
// working: the alias is bound to the SAME handler value, so the two names
// cannot answer differently.
const MethodSearchAlias = "cascade_search"

// QueryParams is recall.query's input.
type QueryParams struct {
	// Query is the search text. Required.
	Query string `json:"query"`
	// Corpus narrows the query to named corpora. Empty means every
	// corpus the session's membership already authorizes.
	Corpus []string `json:"corpus,omitempty"`
	// Scope is the asking session's scope reference. Required.
	Scope string `json:"scope"`
	// Entitlement is the highest privacy tier this query may see. Empty
	// resolves to the project tier.
	Entitlement string `json:"entitlement,omitempty"`
	// K caps the results. Zero uses DefaultK.
	K int `json:"k,omitempty"`
	// Cite requests the rendered Markdown citation block alongside the
	// citations array, which rides the result either way.
	Cite bool `json:"cite,omitempty"`
}

// QueryResult is recall.query's output.
type QueryResult struct {
	// Query echoes the text that was asked.
	Query string `json:"query"`
	// Results are the ranked rows, best first.
	Results []Result `json:"results"`
	// Citations describe those rows, always present.
	Citations []citations.Citation `json:"citations"`
	// Withheld counts rows the scope filter did not authorize. Nothing
	// else about them is reported.
	Withheld int `json:"withheld"`
	// Rendered is the Markdown footnote block, present only when the
	// caller asked to cite.
	Rendered string `json:"rendered,omitempty"`
	// Legs names the retrieval legs that ran.
	Legs []string `json:"legs"`
}

// Handler serves the recall.* namespace over a Service.
//
// # Error contracts
//
// Every refusal is a pkg/cascade taxonomy error, so the RPC layer maps it
// to a wire code without this package knowing the wire at all:
// KindInvalidInput for malformed params, an empty query, a bad k or a
// malformed scope; KindNotFound for a corpus this session cannot read and
// for an index that was never built; KindUnavailable for an index that
// could not be read and for a build with no retrieval leg; KindIntegrity
// for a damaged catalog; KindUnsupported for a catalog this build cannot
// read.
//
// A query that MATCHED nothing is none of those: it is an empty Results
// array and no error at all.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler serving svc.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register binds recall.query and its v1-parity alias on r. This is the
// whole of the composition-root wiring: without this call the handler is
// built, tested and unreachable from a running daemon, which is the
// failure mode this repository's test-only gate exists to make visible.
func (h *Handler) Register(r *rpc.Registry) {
	r.Register(MethodQuery, h.Query)
	r.Register(MethodSearchAlias, h.Query)
}

// Compile-time proof that the method still satisfies the router's handler
// signature, so a drifting signature fails the build here rather than at
// the composition root.
var _ rpc.HandlerFunc = (*Handler)(nil).Query

// Query serves recall.query.
func (h *Handler) Query(ctx context.Context, params json.RawMessage) (any, error) {
	if h.svc == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"recall.query: no recall service is configured on this daemon")
	}
	var p QueryParams
	if err := decodeParams(MethodQuery, params, &p); err != nil {
		return nil, err
	}
	resp, err := h.svc.Query(ctx, Request{
		Query:       p.Query,
		Corpora:     p.Corpus,
		Scope:       p.Scope,
		Entitlement: p.Entitlement,
		K:           p.K,
	})
	if err != nil {
		return nil, err
	}
	return resultFrom(resp, p.Cite), nil
}

// resultFrom shapes the wire result. The rendered block is carried only
// when it was asked for; the citations array is carried always, because a
// caller that cannot see where an answer came from cannot check it.
func resultFrom(resp Response, cite bool) QueryResult {
	out := QueryResult{
		Query:     resp.Query,
		Results:   resp.Results,
		Citations: resp.Citations,
		Withheld:  resp.Withheld,
		Legs:      resp.Legs,
	}
	if out.Results == nil {
		out.Results = []Result{}
	}
	if out.Citations == nil {
		out.Citations = []citations.Citation{}
	}
	if out.Legs == nil {
		out.Legs = []string{}
	}
	if cite {
		out.Rendered = resp.Rendered
	}
	return out
}

// decodeParams unmarshals raw params into dst. Absent params decode as an
// empty object rather than being refused outright; the method still
// validates its own required fields afterwards, so a call with no params
// is answered with "the query is empty" rather than with a parse error
// that says nothing about what was missing.
func decodeParams(method string, params json.RawMessage, dst any) error {
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "%s: malformed params", method)
	}
	return nil
}
