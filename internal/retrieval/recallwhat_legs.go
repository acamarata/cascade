// Purpose: the files and conversation domain legs' own outcome builders,
// split out of recallwhat.go purely for the 300-line cap (same fan-out
// concern; the memory leg's outcome builder lives in
// recallwhat_filter.go, split for the identical reason).
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

package retrieval

import (
	"context"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// whatLegOutcome is one goroutine's contribution to runLegs.
type whatLegOutcome struct {
	domain     string
	list       rrf.RankedList
	meta       map[string]whatCandidate
	err        error
	configured bool
	withheld   int // files only: recall.Service's own authorize() count
}

// runLegs fans req out to the three domain legs, at most maxParallelLegs
// at once, each independent (one leg's error never cancels another).
// Returns the fuse input, merged metadata, the sorted legs that ran, one
// error per domain that was configured but failed, and the files leg's
// own authorization-withheld count.
func (s *RecallWhatService) runLegs(ctx context.Context, req RecallWhatRequest, k int) (
	[]rrf.RankedList, map[string]whatCandidate, []string, map[string]string, int,
) {
	jobs := []func(context.Context) whatLegOutcome{
		func(ctx context.Context) whatLegOutcome { return s.filesOutcome(ctx, req, k) },
		func(ctx context.Context) whatLegOutcome { return s.conversationOutcome(ctx, req, k) },
		func(ctx context.Context) whatLegOutcome { return s.memoryOutcome(ctx, req, k) },
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
	filesWithheld := 0
	for _, r := range out {
		if !r.configured {
			continue
		}
		if r.err != nil {
			domainErrs[r.domain] = r.err.Error()
			continue
		}
		lists = append(lists, r.list)
		ranLegs = append(ranLegs, r.domain)
		if r.domain == DomainFile {
			filesWithheld = r.withheld
		}
		for id, c := range r.meta {
			meta[id] = c
		}
	}
	sort.Strings(ranLegs)
	return lists, meta, ranLegs, domainErrs, filesWithheld
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
	resp, err := s.files.Query(ctx, recall.Request{Query: req.Query, Scope: req.Scope, Entitlement: req.Entitlement, K: k})
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

// conversationOutcome always answers domain-unavailable (D6/Q1 fix,
// confirming review 2026-09-21): conversation.Thread carries no ScopeRef
// (domain.go) and conversation.SearchFilter carries only
// ThreadID/Limit (search.go), so there is no server-side column to narrow
// a conversation search to the caller's resolved session scope the way
// the files and memory legs are narrowed before their own Search runs
// (recallwhat.go's header). Calling SearchTurns anyway would return every
// thread's turns unscoped -- the leak the confirming review reproduced: a
// public thread created under one project is returned to a caller whose
// cwd resolves to a different project. Rather than ship that, the leg
// answers KindUnavailable on every call until a widening ticket adds a
// scope column to Thread/SearchFilter (PCI filed: repo cascade, type
// planning, ScopeRef on Thread and SearchFilter); the leg is restored
// there. SearchTurns/ThreadPrivacy/ListSegments are never invoked -- this
// is a per-request refusal, not a missing-leg condition: a configured
// conversation store still reports KindUnavailable in
// Response.Errors["conversation"] on every call, while remaining absent
// (configured: false, no error) only when no store was injected at all.
func (s *RecallWhatService) conversationOutcome(_ context.Context, _ RecallWhatRequest, _ int) whatLegOutcome {
	if s.conversation == nil {
		return whatLegOutcome{domain: DomainConversation}
	}
	return whatLegOutcome{
		domain:     DomainConversation,
		configured: true,
		err: cascade.New(cascade.KindUnavailable,
			"recall.what: the conversation domain cannot be narrowed to a session scope"),
	}
}
