// Purpose: the recall.what fused, cross-domain query core (R-14.65/66).
// RecallWhatService resolves the caller's SessionScope (E/S-08.T4) before
// any leg runs, then fans one query to the files retrieval index (Epic F,
// reused whole via recall.Service so its RRF fusion/legs/scope filter and
// its own authorize()+citations.Assemble pass are never re-implemented),
// the memory projection's indexed read model (Epic G) and, once it can be
// scope-narrowed, the conversation turns store (Epic T; excluded today, see
// below), then fuses the answering domains' candidates
// via the same shipped internal/retrieval/rrf.FuseWith every other fusion
// path calls. recallwhat_filter.go continues from here: post-fusion
// trust/egress filtering, R-16.7 ranking demotion, hydration and the RPC
// boundary. recallwhat_scope.go and recallwhat_egress.go hold the two
// security seams (session scope, egress substitution) this rework added.
//
// Inputs: a RecallWhatRequest; three injected domain legs (any may be nil
// — a build missing a domain still answers over the rest); an injected
// ScopeResolver and EgressSubstitutor (both required — a service missing
// either can only fail closed on every call, so construction refuses
// rather than shipping a firewall nobody wired). Outputs: a
// RecallWhatResponse, or a taxonomy error.
//
// Constraints: each leg runs in its own goroutine, bounded by a local
// semaphore (no governor transit — R-14.66: local, not a Conductor
// dispatch); one leg's error is per-domain, never total. The caller's
// `scope` and `tier` are never trusted alone: scope is resolved server-
// side and a caller-asserted value is refused on mismatch (D5.1); there
// is no caller-supplied tier at all any more (D5's fix item 2/7) — every
// admission and redaction decision is taken by the real egress boundary
// in recallwhat_egress.go.
//
// KNOWN LIMIT, recorded rather than silently worked around: conversation
// .Thread carries no ScopeRef field (domain.go) and SearchFilter carries
// only ThreadID/Limit, so the conversation leg cannot be narrowed to the
// resolved scope the way files and memory are. It is therefore EXCLUDED:
// conversationOutcome (recallwhat_legs.go) answers a typed KindUnavailable
// error that lands in Errors["conversation"] and never searches. The
// widening ticket (ScopeRef on Thread + SearchFilter) is PCI
// s47t1-conversation-scope-ref-missing; the leg is restored there.
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

package retrieval

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MethodWhat is the recall.what JSON-RPC method name (R-14.65/66).
const MethodWhat = "recall.what"

// The three closed domain names a Result.Domain/Response.Legs/Errors
// entry may carry.
const (
	DomainFile         = "file"
	DomainConversation = "conversation"
	DomainMemory       = "memory"
)

// whatStrategy* name each domain's RankedList in the top-level fuse —
// distinct from rrf.StrategyFTS/StrategyVector, the sub-legs recall.Service
// already fused internally into the single "file" list handed in here.
const (
	whatStrategyFile         rrf.StrategyName = "file"
	whatStrategyConversation rrf.StrategyName = "conversation"
	whatStrategyMemory       rrf.StrategyName = "memory"
)

// maxParallelLegs bounds the local fan-out; sized to the fixed domain
// count so a future fourth leg still runs bounded, not serially.
const maxParallelLegs = 3

// whatSnippetMaxRunes caps a hydrated snippet so one long body cannot
// dominate the response.
const whatSnippetMaxRunes = 240

// RecallWhatFilesLeg is the files domain: recall.Service's own Query
// method. *recall.Service satisfies it with no adapter.
type RecallWhatFilesLeg interface {
	Query(ctx context.Context, req recall.Request) (recall.Response, error)
}

// RecallWhatConversationLeg is the turns/threads domain: the three
// conversation.Store methods read here. conversation.Store satisfies it.
type RecallWhatConversationLeg interface {
	SearchTurns(ctx context.Context, query string, filter conversation.SearchFilter) ([]conversation.TurnMatch, error)
	ListSegments(ctx context.Context, turnID string) ([]conversation.Segment, error)
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
}

// RecallWhatMemoryLeg is the memory domain: the projection's own indexed
// read model (D2 -- the closed-ticket generator refuses to retire the
// NewProjectionJob allow entry while nothing production calls it, and
// Handler.Recall-as-search is a file scan, not the indexed read model the
// contract pre-seeded). *memory.ProjectionJob satisfies it with no
// adapter. SearchIncludingExpired, not Search, is the method this leg
// calls (D6/Q6): R-16.7 demotes an expired MemoryEntry rather than
// excluding it, which needs the row in hand to demote against.
type RecallWhatMemoryLeg interface {
	SearchIncludingExpired(ctx context.Context, query string, limit int) ([]memory.IndexedRecord, error)
}

// RecallWhatRequest is one recall.what query, after decoding off the
// wire. ResolveIn carries the caller's raw session signals
// (scope.ResolveInput); Scope, when non-empty, is the caller's OWN claim
// about its resolved scope, checked against resolution and refused on
// disagreement (recallwhat_scope.go) -- it narrows nothing on its own. K
// zero uses recall.DefaultK. There is no caller-supplied sensitivity
// tier: every admission decision is taken by the real egress boundary
// (recallwhat_egress.go), never by a value the caller could assert.
type RecallWhatRequest struct {
	Query       string
	Scope       string
	Entitlement string
	K           int
	ResolveIn   scope.ResolveInput
}

// RecallWhatResult is one fused answer row.
type RecallWhatResult struct {
	Rank       int                `json:"rank"`
	Domain     string             `json:"domain"`
	ID         string             `json:"id"`
	Path       string             `json:"path,omitempty"`
	CorpusID   string             `json:"corpus_id,omitempty"`
	Trust      corpus.TrustLevel  `json:"trust"`
	Score      float64            `json:"score"`
	RawScore   float64            `json:"raw_score"`
	Strategies []rrf.StrategyName `json:"strategies,omitempty"`
	Snippet    string             `json:"snippet,omitempty"`
}

// RecallWhatResponse is one recall.what answer. Errors carries one entry
// per domain that was configured but failed (Domain-unavailable
// acceptance criterion): a partial answer, never a total failure, as long
// as one domain answered. Withheld counts rows dropped by an
// authorization or privacy decision (scope, trust, or the egress
// boundary); Truncated counts rows dropped only because there were more
// than K -- the two are never the same count, and the CLI reports them
// with different words (fix item 4).
type RecallWhatResponse struct {
	Query     string               `json:"query"`
	Results   []RecallWhatResult   `json:"results"`
	Citations []citations.Citation `json:"citations"`
	Withheld  int                  `json:"withheld"`
	Truncated int                  `json:"truncated,omitempty"`
	Rendered  string               `json:"rendered,omitempty"`
	Legs      []string             `json:"legs"`
	Errors    map[string]string    `json:"errors,omitempty"`
}

// whatCandidate is one domain's record behind a fused row, kept by chunk
// id for recallwhat_filter.go's filter/demote/hydrate passes.
type whatCandidate struct {
	rrf.Candidate
	Domain     string
	Snippet    string
	ThreadTier provider.SensitivityTier // conversation only
	Supersedes string                   // memory only: address this entry replaces
	Expired    bool                     // memory only
	// FilesChunkID/FileCitation carry the files leg's OWN already-
	// authorized citation forward (recall.Service.Query's authorize()+
	// citations.Assemble already ran over the real ScopeFilter) rather
	// than re-deriving one from candidate data with no resolver behind
	// it (fix item 3). HasFileCitation is false when recall.Service's
	// own authorize() pass withheld this row -- it must be withheld
	// here too, never re-admitted.
	FilesChunkID    string
	FileCitation    citations.Citation
	HasFileCitation bool
}

// RecallWhatService fans out to its three domain legs and fuses the answer.
type RecallWhatService struct {
	files        RecallWhatFilesLeg
	conversation RecallWhatConversationLeg
	memory       RecallWhatMemoryLeg
	params       rrf.Params
	clock        runtime.Clock
	resolver     ScopeResolver
	egress       EgressSubstitutor
}

// NewRecallWhatService builds a Service; any DOMAIN leg may be nil (nil-
// nil-nil is refused). resolver and sub are the two security seams this
// rework added and are both required: a service built without either
// could only ever fail closed on every request, and shipping that as a
// silent partial construction is worse than refusing to build it.
func NewRecallWhatService(
	files RecallWhatFilesLeg, conv RecallWhatConversationLeg, mem RecallWhatMemoryLeg,
	params rrf.Params, clock runtime.Clock, resolver ScopeResolver, sub EgressSubstitutor,
) (*RecallWhatService, error) {
	if files == nil && conv == nil && mem == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "recall.what: no domain leg configured")
	}
	if resolver == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "recall.what: no scope resolver configured")
	}
	if sub == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "recall.what: no egress boundary configured")
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &RecallWhatService{
		files: files, conversation: conv, memory: mem, params: params, clock: clock,
		resolver: resolver, egress: sub,
	}, nil
}

// Query runs one fused, cross-domain recall.
func (s *RecallWhatService) Query(ctx context.Context, req RecallWhatRequest) (RecallWhatResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return RecallWhatResponse{}, cascade.New(cascade.KindInvalidInput, "recall.what: the query is empty")
	}
	k, err := whatEffectiveK(req.K)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	scopeRef, err := resolveRequestScope(ctx, s.resolver, req)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	req.Scope = scopeRef
	lists, meta, ranLegs, domainErrs, filesWithheld := s.runLegs(ctx, req, k)
	if len(lists) == 0 {
		return RecallWhatResponse{}, cascade.New(cascade.KindUnavailable,
			"recall.what: no domain answered; every configured domain is unavailable")
	}
	fused, err := rrf.FuseWith(lists, s.params)
	if err != nil {
		return RecallWhatResponse{}, err
	}
	return s.respondFromFused(ctx, req, fused, meta, k, ranLegs, domainErrs, filesWithheld)
}

// whatLegOutcome, runLegs, filesOutcome, conversationOutcome and
// conversationTrust live in recallwhat_legs.go; turnSnippet/
// truncateRunes/memoryOutcome live in recallwhat_filter.go (same package;
// the split is purely for the 300-line cap).

// whatEffectiveK mirrors recall.DefaultK/MaxK so both commands cap alike.
func whatEffectiveK(k int) (int, error) {
	switch {
	case k < 0:
		return 0, cascade.Newf(cascade.KindInvalidInput, "recall.what: k must not be negative, got %d", k)
	case k == 0:
		return recall.DefaultK, nil
	case k > recall.MaxK:
		return 0, cascade.Newf(cascade.KindInvalidInput, "recall.what: k must be at most %d, got %d", recall.MaxK, k)
	}
	return k, nil
}
