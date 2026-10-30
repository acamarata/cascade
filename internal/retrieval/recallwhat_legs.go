// Purpose: the files and conversation domain legs' own outcome builders,
// split out of recallwhat.go purely for the 300-line cap (same fan-out
// concern; the memory leg's outcome builder lives in
// recallwhat_filter.go, split for the identical reason).
//
// Constraints: the conversation leg resolves the caller's scope itself and
// searches only with that resolved project ref and ProjectSet, so the
// store narrows in SQL before its limit; a resolution or search error
// refuses the whole leg and never falls back to an unscoped search. Every
// candidate crosses the egress boundary at its own thread's tier before it
// is ranked. Refusal reasons never carry turn content.
//
// SPORT: internal.retrieval.RecallWhatService/CHANGED (conversation leg
// restored with project and principal scope).

package retrieval

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// whatLegOutcome is one goroutine's contribution to runLegs.
type whatLegOutcome struct {
	domain     string
	list       rrf.RankedList
	meta       map[string]whatCandidate
	err        error
	note       error // a non-fatal per-candidate failure: reported, the list still ranks
	configured bool
	withheld   int // rows this leg's own authorization or egress pass dropped
}

// runLegs fans req out to the three domain legs, at most maxParallelLegs
// at once, each independent (one leg's error never cancels another).
// Returns the fuse input, merged metadata, the sorted legs that ran, one
// error per domain that was configured but failed (or answered with a
// per-candidate note), and the rows the legs' own authorization and
// egress passes withheld. The files and memory legs see store (from
// storeScopeFor) as their Scope and general flag; the conversation leg
// keeps req.
func (s *RecallWhatService) runLegs(ctx context.Context, req RecallWhatRequest, store storeScope, k int) (
	[]rrf.RankedList, map[string]whatCandidate, []string, map[string]string, int,
) {
	storeReq := req
	storeReq.Scope, storeReq.general = store.ref, store.general
	jobs := []func(context.Context) whatLegOutcome{
		func(ctx context.Context) whatLegOutcome { return s.filesOutcome(ctx, storeReq, k) },
		func(ctx context.Context) whatLegOutcome { return s.conversationOutcome(ctx, req, k) },
		func(ctx context.Context) whatLegOutcome { return s.memoryOutcome(ctx, storeReq, k) },
	}
	sem := make(chan struct{}, maxParallelLegs)
	var wg sync.WaitGroup
	out := make([]whatLegOutcome, len(jobs))
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, job func(context.Context) whatLegOutcome) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = job(ctx)
		}(i, job)
	}
	wg.Wait()

	var lists []rrf.RankedList
	meta := make(map[string]whatCandidate)
	var ranLegs []string
	domainErrs := make(map[string]string)
	legWithheld := 0
	for _, r := range out {
		if !r.configured {
			continue
		}
		if r.err != nil {
			domainErrs[r.domain] = r.err.Error()
			continue
		}
		if r.note != nil {
			domainErrs[r.domain] = r.note.Error()
		}
		lists = append(lists, r.list)
		ranLegs = append(ranLegs, r.domain)
		legWithheld += r.withheld
		for id, c := range r.meta {
			meta[id] = c
		}
	}
	sort.Strings(ranLegs)
	return lists, meta, ranLegs, domainErrs, legWithheld
}

// filesOutcome queries files via recall.Service, reusing its scope
// filter, RRF fusion AND its own authorize()+citations.Assemble pass
// whole (fix item 3): the citation this leg produced for a chunk is kept
// verbatim rather than re-derived, and a chunk recall.Service's own
// authorize() withheld is never re-admitted here.
func (s *RecallWhatService) filesOutcome(ctx context.Context, req RecallWhatRequest, k int) whatLegOutcome {
	if s.files == nil {
		return whatLegOutcome{domain: DomainFile}
	}
	resp, err := s.files.Query(ctx, recall.Request{
		Query: req.Query, Scope: req.Scope, Entitlement: req.Entitlement, K: k, GeneralSession: req.general,
	})
	if err != nil {
		return whatLegOutcome{domain: DomainFile, err: err, configured: true}
	}
	citeByChunk := make(map[string]citations.Citation, len(resp.Citations))
	for _, c := range resp.Citations {
		citeByChunk[c.ChunkID] = c
	}
	list := rrf.RankedList{Strategy: whatStrategyFile, Weight: rrf.NeutralWeight}
	meta := make(map[string]whatCandidate, len(resp.Results))
	for _, r := range resp.Results {
		id := DomainFile + ":" + r.ChunkID
		list.Hits = append(list.Hits, rrf.Candidate{ChunkID: id, Path: r.Path, CorpusID: r.CorpusID, Trust: r.Trust})
		cite, ok := citeByChunk[r.ChunkID]
		meta[id] = whatCandidate{
			Candidate: list.Hits[len(list.Hits)-1], Domain: DomainFile,
			FilesChunkID: r.ChunkID, FileCitation: cite, HasFileCitation: ok,
		}
	}
	return whatLegOutcome{domain: DomainFile, list: list, meta: meta, configured: true, withheld: resp.Withheld}
}

// errConvTierRead and errConvSegments are the conversation leg's
// per-candidate notes. Neither names a thread, a turn or any content.
var (
	errConvTierRead = cascade.New(cascade.KindUnavailable,
		"recall.what: a conversation thread's privacy tier could not be read; its turns were dropped")
	errConvSegments = cascade.New(cascade.KindUnavailable,
		"recall.what: a conversation turn's content could not be read; it was dropped")
)

// conversationOutcome searches the conversation store narrowed to the
// caller's RESOLVED project (ProjectSet, the general bucket included) and
// admits each hit through admitTurns. It resolves the scope itself rather
// than trusting req.Scope, and a resolution or search error refuses the
// whole leg: no unscoped search is ever attempted. A principal caller's
// attribution narrowing is added by the composition-root adapter.
func (s *RecallWhatService) conversationOutcome(ctx context.Context, req RecallWhatRequest, k int) whatLegOutcome {
	if s.conversation == nil {
		return whatLegOutcome{domain: DomainConversation}
	}
	refused := func(err error) whatLegOutcome {
		return whatLegOutcome{domain: DomainConversation, err: err, configured: true}
	}
	ref, err := resolveRequestScope(ctx, s.resolver, req)
	if err != nil {
		return refused(err)
	}
	matches, err := s.conversation.SearchTurns(ctx, req.Query,
		RecallWhatSearchFilter{ProjectRef: ref, ProjectSet: true, Limit: k})
	if err != nil {
		return refused(err)
	}
	return s.admitTurns(ctx, matches)
}

// admitTurns ranks each hit in store order. A hit whose thread tier or
// content cannot be read is dropped with a note; a hit the egress boundary
// refuses at its thread's tier is dropped and counted withheld. A turn
// marked untrusted is ranked corpus.TrustUntrustedSource.
func (s *RecallWhatService) admitTurns(ctx context.Context, matches []RecallWhatTurnMatch) whatLegOutcome {
	out := whatLegOutcome{domain: DomainConversation, configured: true,
		list: rrf.RankedList{Strategy: whatStrategyConversation, Weight: rrf.NeutralWeight},
		meta: make(map[string]whatCandidate, len(matches))}
	tiers := make(map[string]provider.SensitivityTier)
	for _, m := range matches {
		tier, err := s.threadTier(ctx, tiers, m.Turn.ThreadID)
		if err != nil {
			out.note = firstNote(out.note, errConvTierRead)
			continue
		}
		snippet, err := s.turnSnippet(ctx, m.Turn.ID)
		if err != nil {
			out.note = firstNote(out.note, errConvSegments)
			continue
		}
		redacted, err := s.egress.InterceptClass(ctx, egress.EgressClassRecallWhat, egressTierFor(tier), []byte(snippet))
		if err != nil {
			out.withheld++
			continue
		}
		cand := rrf.Candidate{ChunkID: DomainConversation + ":" + m.Turn.ID, Path: m.Turn.ThreadID,
			CorpusID: DomainConversation, Trust: conversationTrust(m.Turn)}
		out.list.Hits = append(out.list.Hits, cand)
		out.meta[cand.ChunkID] = whatCandidate{Candidate: cand, Domain: DomainConversation,
			Snippet: string(redacted), ThreadTier: tier}
	}
	return out
}

// firstNote keeps the first per-candidate note a leg call recorded.
func firstNote(have, next error) error {
	if have != nil {
		return have
	}
	return next
}

// threadTier reads a thread's privacy tier once per leg call.
func (s *RecallWhatService) threadTier(ctx context.Context, cache map[string]provider.SensitivityTier, threadID string) (provider.SensitivityTier, error) {
	if tier, ok := cache[threadID]; ok {
		return tier, nil
	}
	tier, err := s.conversation.ThreadPrivacy(ctx, threadID)
	if err != nil {
		return tier, err
	}
	cache[threadID] = tier
	return tier, nil
}

// turnSnippet joins a turn's segment contents, capped at
// whatSnippetMaxRunes.
func (s *RecallWhatService) turnSnippet(ctx context.Context, turnID string) (string, error) {
	segs, err := s.conversation.ListSegments(ctx, turnID)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		parts = append(parts, seg.Content)
	}
	return truncateRunes(strings.Join(parts, " "), whatSnippetMaxRunes), nil
}

// conversationTrust ranks an untrusted-provenance turn as untrusted source.
func conversationTrust(t RecallWhatTurn) corpus.TrustLevel {
	if t.Untrusted {
		return corpus.TrustUntrustedSource
	}
	return corpus.TrustTrusted
}
