package evidence

// Purpose: the evidence INDEX -- the addressable view over the jobs_claim
// table this ticket's every other AQ ticket names as source_index_ref.
// Inputs: a run id (IndexURI) or an index URI (ParseIndexURI/ResolveIndex).
// Outputs: the "evidence://<run_id>" URI string, its inverse, or the
// ordered claim list it addresses.
// Constraints: ResolveIndex orders by claim id so two resolutions of the
// same URI are byte-identical; invalidated claims are returned with
// InvalidatedAt set rather than dropped -- the index never silently loses
// a row.
//
// SPORT: evidence/claim-record (ADD).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

const indexScheme = "evidence://"

// IndexURI returns the evidence index address for runID: the
// source_index_ref value ExecutiveEvidenceBriefV1 (AQ/S-83.T2) writes.
func IndexURI(runID string) string {
	return indexScheme + runID
}

// ParseIndexURI is IndexURI's inverse. Any scheme other than
// "evidence://", or an empty run id, is a typed invalid-input error.
func ParseIndexURI(uri string) (string, error) {
	if !strings.HasPrefix(uri, indexScheme) {
		return "", cascade.Newf(cascade.KindInvalidInput, "evidence: unrecognized index uri %q: must be evidence://<run_id>", uri)
	}
	runID := uri[len(indexScheme):]
	if runID == "" {
		return "", cascade.New(cascade.KindInvalidInput, "evidence: empty run id in index uri")
	}
	return runID, nil
}

// ResolveIndex returns every claim whose ProducedBy.RunID matches uri's run
// id, ordered by claim id so two resolutions of the same URI are
// byte-identical. Invalidated claims are included, carrying InvalidatedAt.
func (s *Store) ResolveIndex(ctx context.Context, uri string) ([]Claim, error) {
	runID, err := ParseIndexURI(uri)
	if err != nil {
		return nil, err
	}
	return s.ClaimsByRun(ctx, runID)
}
