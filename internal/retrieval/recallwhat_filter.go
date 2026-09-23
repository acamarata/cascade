// Purpose: the memory domain leg (over ProjectionJob.SearchIncludingExpired,
// D2/D6, so expired rows can be demoted rather than hidden), the truncateRunes hydration helper, and the post-fusion
// exclusion + R-16.7 demotion passes recallwhat.go's Query hands off to.
// recallwhat_redact.go continues from here: the per-field egress
// substitution pass, citation/result assembly, and the RPC boundary. (The
// conversation leg's own snippet hydration is gone: recallwhat_legs.go's
// conversationOutcome answers KindUnavailable unconditionally -- D6/Q1 --
// so it never reaches this file's filter/demote passes at all.)
//
// Inputs: the fused []rrf.FusedResult, plus whatCandidate metadata by id.
// Outputs: the filtered/demoted rows recallwhat_redact.go hydrates into a
// RecallWhatResponse.
//
// Constraints: TWO exclusion rules run here, both unconditional -- fix
// item 6 removed the caller-declared-Tier trust problem, so neither rule
// depends on anything the caller asserts about itself any more. (1)
// TrustUntrustedSource content is ALWAYS excluded from a fused recall.what
// answer, not merely from a caller in a "privileged" context: a caller-
// asserted context was exactly the value the adversarial review found
// forgeable, so this rework drops the condition rather than trying to
// re-derive it from something else the caller could still assert.
// (2) a conversation thread whose own privacy tier the real egress
// boundary refuses (recallwhat_egress.go: EgressClassRecallWhat admits
// only internal/public) is excluded -- reachable today only if a future
// ticket restores the conversation leg (D6/Q1's PCI); the check stays so
// that restoration does not silently drop it. A files-domain row
// recall.Service's own authorize() pass did not produce a citation for is
// excluded here too -- never re-admitted past its own leg's authorization.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

package retrieval

import (
	"context"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

// memoryOutcome queries the memory projection's indexed read model (D2),
// narrowed to req.Scope BEFORE the projection's own k cap applies
// (P1-E07-W5-S92-T1, PCI s47t1-memory-scope-after-k) rather than after
// fetching k rows and filtering: the old order could lose an in-scope row
// ranked past k by more out-of-scope rows. The equality check below stays
// as a second, fail-closed line of defense (R-16.7) -- never trusting the
// projection's own narrowing alone to be the only thing standing between
// an out-of-scope row and the caller. Trust stays TrustTrusted
// unconditionally: memory has no external-import path (Origin is
// session|file|harness, all first-party, types.go), so there is no
// record-derived signal that would ever justify tagging one
// TrustUntrustedSource, unlike a conversation turn's role -- moot in
// practice today since the conversation leg itself is excluded
// (recallwhat_legs.go's conversationOutcome, D6/Q1).
func (s *RecallWhatService) memoryOutcome(ctx context.Context, req RecallWhatRequest, k int) whatLegOutcome {
	if s.memory == nil {
		return whatLegOutcome{domain: DomainMemory}
	}
	// SearchInScopeIncludingExpired, not SearchIncludingExpired (D6/Q6 +
	// P1-E07-W5-S92-T1): an expired row must still reach this leg so
	// demoteSupersededAndExpired below has a row to demote against, and
	// req.Scope narrows the projection's own search before its k cap
	// applies rather than after.
	rows, err := s.memory.SearchInScopeIncludingExpired(ctx, req.Query, req.Scope, k)
	if err != nil {
		return whatLegOutcome{domain: DomainMemory, err: err, configured: true}
	}
	now := s.clock.Now().UTC()
	list := rrf.RankedList{Strategy: whatStrategyMemory, Weight: rrf.NeutralWeight}
	meta := make(map[string]whatCandidate, len(rows))
	for _, r := range rows {
		// Deleted (tombstoned) rows are gone, never a candidate at all.
		// An EXPIRED row is still a candidate: R-16.7 demotes it below
		// its non-expired peers (demoteSupersededAndExpired), it does
		// not exclude it outright -- so expiry is carried as a flag
		// here, not folded into the same skip Deleted uses. The scope
		// check is now redundant with the projection's own pre-limit
		// filter in the common case, and stays anyway (fail-closed,
		// R-16.7): a bug in one layer must not be the only thing
		// standing between an out-of-scope row and the caller.
		if r.Deleted || r.ScopeRef != req.Scope {
			continue
		}
		id := DomainMemory + ":" + r.ID
		cand := rrf.Candidate{ChunkID: id, Path: string(r.Kind) + "/" + r.Name, CorpusID: DomainMemory, Trust: corpus.TrustTrusted}
		list.Hits = append(list.Hits, cand)
		meta[id] = whatCandidate{
			Candidate: cand, Domain: DomainMemory, Snippet: truncateRunes(r.Body, whatSnippetMaxRunes),
			Supersedes: memorySupersedesID(r.Supersedes), Expired: r.Expired(now),
		}
	}
	return whatLegOutcome{domain: DomainMemory, list: list, meta: meta, configured: true}
}

// memorySupersedesID: "<kind>/<name>" ref -> domain-prefixed chunk id.
func memorySupersedesID(ref string) string {
	if ref == "" {
		return ""
	}
	kind, name, err := memory.ParseAddress(ref)
	if err != nil {
		return ""
	}
	return DomainMemory + ":" + memory.Address(kind, name)
}

// truncateRunes returns s capped at max runes, rune-safe.
func truncateRunes(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit])
	}
	return s
}

// filterOutcome is filterFusedResults' return: the surviving rows, the
// meta map with conversation snippets already substituted (redaction is
// combined with the exclusion check itself -- one InterceptClass call
// decides both), and the count excluded by this pass.
type filterOutcome struct {
	fused    []rrf.FusedResult
	meta     map[string]whatCandidate
	withheld int
}

// filterFusedResults drops rows either exclusion rule withholds (file
// header) and substitutes conversation snippets through the egress
// boundary in the same pass.
func filterFusedResults(ctx context.Context, sub EgressSubstitutor, fused []rrf.FusedResult, meta map[string]whatCandidate) filterOutcome {
	out := filterOutcome{meta: make(map[string]whatCandidate, len(meta))}
	for _, r := range fused {
		c, ok := meta[r.ChunkID]
		if !ok {
			out.withheld++
			continue
		}
		if r.Trust == corpus.TrustUntrustedSource {
			out.withheld++
			continue
		}
		if c.Domain == DomainFile && !c.HasFileCitation {
			out.withheld++
			continue
		}
		if c.Domain == DomainConversation {
			redacted, admitted := substituteField(ctx, sub, egressTierFor(c.ThreadTier), c.Snippet)
			if !admitted {
				out.withheld++
				continue
			}
			c.Snippet = redacted
		}
		out.fused = append(out.fused, r)
		out.meta[r.ChunkID] = c
	}
	return out
}

// demoteSupersededAndExpired applies R-16.7 as two stable partitions over
// the existing RRF order; nothing is re-scored.
func demoteSupersededAndExpired(fused []rrf.FusedResult, meta map[string]whatCandidate) []rrf.FusedResult {
	rankOf := make(map[string]int, len(fused))
	for i, r := range fused {
		rankOf[r.ChunkID] = i
	}
	var live, expired []rrf.FusedResult
	for _, r := range fused {
		if meta[r.ChunkID].Expired {
			expired = append(expired, r)
		} else {
			live = append(live, r)
		}
	}
	return append(demoteSuperseded(live, meta, rankOf), expired...)
}

// demoteSuperseded reinserts a superseded entry just after its superseder.
func demoteSuperseded(fused []rrf.FusedResult, meta map[string]whatCandidate, rankOf map[string]int) []rrf.FusedResult {
	supersedes := make(map[string]string, len(fused)) // superseder id -> superseded id
	skip := make(map[string]bool, len(fused))         // superseded ids, skipped at their natural rank
	for _, r := range fused {
		target := meta[r.ChunkID].Supersedes
		if _, present := rankOf[target]; target == "" || !present {
			continue
		}
		supersedes[r.ChunkID] = target
		skip[target] = true
	}
	if len(skip) == 0 {
		return fused
	}
	byID := make(map[string]rrf.FusedResult, len(fused))
	for _, r := range fused {
		byID[r.ChunkID] = r
	}
	out := make([]rrf.FusedResult, 0, len(fused))
	for _, r := range fused {
		if skip[r.ChunkID] {
			continue
		}
		out = append(out, r)
		if victim, ok := supersedes[r.ChunkID]; ok {
			out = append(out, byID[victim])
		}
	}
	return out
}
