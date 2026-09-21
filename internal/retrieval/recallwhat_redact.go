// Purpose: the final hydration pass recallwhat_filter.go's filtered,
// demoted rows go through before they become a RecallWhatResponse: every
// remaining outbound string (a path, a memory snippet, a domain error, the
// rendered citation block) transits the real egress boundary
// (recallwhat_egress.go) at TierInternal -- the tier EgressClassRecallWhat
// is always configured to admit, so a refusal here means the firewall
// itself is unavailable and fails the whole request (fix item 5: every
// error path is a test, never a silently-emitted unsanitized field).
//
// A conversation row's Snippet is the one exception: recallwhat_filter.go
// already ran it through the boundary at the row's OWN ThreadTier (the
// exclusion decision and the redaction are the same InterceptClass call),
// so it is used here as-is rather than substituted a second time.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

package retrieval

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

// respondFromFused applies the exclusion/demotion passes, the k cap, and
// hydrates the final response. Withheld is the authorization/privacy
// count (the files leg's own recall.Service.Query withheld count plus
// every row this pass excluded); Truncated is the separate k-overflow
// count (fix item 4) -- the CLI reports the two with different words.
func (s *RecallWhatService) respondFromFused(
	ctx context.Context, req RecallWhatRequest, fused []rrf.FusedResult, meta map[string]whatCandidate,
	k int, ranLegs []string, domainErrs map[string]string, filesWithheld int,
) (RecallWhatResponse, error) {
	filtered := filterFusedResults(ctx, s.egress, fused, meta)
	demoted := demoteSupersededAndExpired(filtered.fused, filtered.meta)
	truncated := 0
	if len(demoted) > k {
		truncated = len(demoted) - k
		demoted = demoted[:k]
	}
	results, cites, err := s.buildResults(ctx, demoted, filtered.meta)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	scrubbedErrs, err := s.scrubErrors(ctx, domainErrs)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	rendered, err := substituteInternal(ctx, s.egress, citations.Render(citations.CitationSet{Citations: cites}).Definitions)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	return RecallWhatResponse{
		Query: req.Query, Results: results, Citations: cites,
		Withheld: filtered.withheld + filesWithheld, Truncated: truncated,
		Rendered: rendered, Legs: ranLegs, Errors: scrubbedErrs,
	}, nil
}

// buildResults turns fused rows into Results/Citations, substituting
// every outbound Path (every domain) and every non-conversation Snippet
// through the egress boundary at TierInternal.
func (s *RecallWhatService) buildResults(ctx context.Context, fused []rrf.FusedResult, meta map[string]whatCandidate) ([]RecallWhatResult, []citations.Citation, error) {
	results := make([]RecallWhatResult, 0, len(fused))
	cites := make([]citations.Citation, 0, len(fused))
	for i, r := range fused {
		rank := i + 1
		c := meta[r.ChunkID]
		// The chunk id itself carries a caller-chosen memory key
		// ("<domain>:<kind>/<name>") or a conversation/file identity --
		// substituted like every other outbound string rather than
		// assumed safe because it looks like an identifier (fix item 5:
		// "memory key" is its own named leak surface, distinct from
		// Path).
		id, err := substituteInternal(ctx, s.egress, r.ChunkID)
		if err != nil {
			return nil, nil, err
		}
		path, err := substituteInternal(ctx, s.egress, r.Path)
		if err != nil {
			return nil, nil, err
		}
		snippet := c.Snippet
		if c.Domain != DomainConversation {
			if snippet, err = substituteInternal(ctx, s.egress, snippet); err != nil {
				return nil, nil, err
			}
		}
		results = append(results, RecallWhatResult{
			Rank: rank, Domain: c.Domain, ID: id, Path: path, CorpusID: r.CorpusID,
			Trust: r.Trust, Score: r.Score, RawScore: r.RawScore, Strategies: r.Strategies, Snippet: snippet,
		})
		cites = append(cites, citationForRow(r, rank, c, id, path))
	}
	return results, cites, nil
}

// citationForRow builds one row's citation. A file-domain row reuses its
// own leg's already-authorized citation (fix item 3) rather than
// re-deriving one with no resolver behind it; every other domain builds
// its citation directly, since neither conversation nor memory has a
// corpus.Record a citations.SourceResolver could resolve against.
func citationForRow(r rrf.FusedResult, rank int, c whatCandidate, id, path string) citations.Citation {
	if c.Domain == DomainFile {
		cite := c.FileCitation
		cite.Rank = rank
		cite.ChunkID = id
		cite.Path = path
		return cite
	}
	return citations.Citation{
		ChunkID: id, Path: path, CorpusID: r.CorpusID, Trust: r.Trust,
		Rank: rank, Score: r.Score, RawScore: r.RawScore, Strategies: r.Strategies,
	}
}

// scrubErrors substitutes every domain error message through the egress
// boundary: a raw err.Error() can carry a machine path or a driver's
// exact-value detail, and this is the one place every domain's error
// text reaches the wire (fix item 5).
func (s *RecallWhatService) scrubErrors(ctx context.Context, domainErrs map[string]string) (map[string]string, error) {
	if len(domainErrs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(domainErrs))
	for domain, msg := range domainErrs {
		scrubbed, err := substituteInternal(ctx, s.egress, msg)
		if err != nil {
			return nil, err
		}
		out[domain] = scrubbed
	}
	return out, nil
}
