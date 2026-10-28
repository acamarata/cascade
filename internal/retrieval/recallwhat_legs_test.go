package retrieval

// Purpose: the restored conversation leg's proofs: it searches only with
// the RESOLVED project ref and ProjectSet (the general bucket included),
// refuses the whole leg on a resolution error without any search, shapes
// each row as conversation:<turn_id> at its thread id, admits every
// candidate through the real egress engine at its thread's tier, and ranks
// an untrusted turn as untrusted source. scopedConvLeg narrows exactly as
// conversation.SearchTurns does in SQL (a non-empty ref or ProjectSet
// narrows; a thread with no project row never matches), before its limit.
// The store's own SQL is proven in internal/conversation and, end to end
// over a real store, in cmd/cascade.
//
// SPORT: internal.retrieval.RecallWhatService/CHANGED (conversation leg).

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// scopedTurn is one stored turn and its thread's project row (nil: none).
type scopedTurn struct {
	match   RecallWhatTurnMatch
	project *string
}

// scopedConvLeg is a conversation store double that records every filter
// it receives and narrows like the SQL, in store rank order, then limits.
type scopedConvLeg struct {
	turns   []scopedTurn
	tiers   map[string]provider.SensitivityTier
	tierErr map[string]error
	calls   []RecallWhatSearchFilter
}

func (f *scopedConvLeg) SearchTurns(_ context.Context, _ string, filter RecallWhatSearchFilter) ([]RecallWhatTurnMatch, error) {
	f.calls = append(f.calls, filter)
	var out []RecallWhatTurnMatch
	for _, t := range f.turns {
		if (filter.ProjectSet || filter.ProjectRef != "") && (t.project == nil || *t.project != filter.ProjectRef) {
			continue
		}
		if out = append(out, t.match); filter.Limit > 0 && len(out) == filter.Limit {
			break
		}
	}
	return out, nil
}

func (f *scopedConvLeg) ListSegments(_ context.Context, turnID string) ([]RecallWhatSegment, error) {
	return []RecallWhatSegment{{Content: "phrase from " + turnID}}, nil
}

func (f *scopedConvLeg) ThreadPrivacy(_ context.Context, threadID string) (provider.SensitivityTier, error) {
	return f.tiers[threadID], f.tierErr[threadID]
}

func ref(s string) *string { return &s }

// turnOn builds a stored turn on thread th (project p) at rank r.
func turnOn(id, th string, p *string, r float64) scopedTurn {
	return scopedTurn{match: RecallWhatTurnMatch{Turn: RecallWhatTurn{ID: id, ThreadID: th, Role: "user"}, Rank: r}, project: p}
}

// mixedProjectLeg: project B and no-project turns outrank project A's and
// the general bucket's, every thread public.
func mixedProjectLeg() *scopedConvLeg {
	return &scopedConvLeg{
		turns: []scopedTurn{
			turnOn("b1", "thB", ref("projB"), 1), turnOn("n1", "thNone", nil, 2), turnOn("b2", "thB", ref("projB"), 3),
			turnOn("a1", "thA", ref("proj1"), 4), turnOn("g1", "thGen", ref(""), 5),
		},
		tiers: map[string]provider.SensitivityTier{"thA": provider.SensitivityPublic, "thB": provider.SensitivityPublic,
			"thNone": provider.SensitivityPublic, "thGen": provider.SensitivityPublic},
	}
}

// convService builds a service with only the conversation leg and the real egress engine.
func convService(t *testing.T, conv RecallWhatConversationLeg, resolver ScopeResolver) *RecallWhatService {
	t.Helper()
	svc, err := NewRecallWhatService(nil, conv, nil, rrf.Params{}, testClock(), resolver, testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	return svc
}

// convIDs returns the conversation rows' ids in rank order.
func convIDs(resp RecallWhatResponse) []string {
	var ids []string
	for _, r := range resp.Results {
		if r.Domain == DomainConversation {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// TestConversationLeg_ScopeEnforced: a caller resolved to project A (or to
// the general bucket) gets only that project's turns, even though project B
// and no-project turns outrank them and k is 1; every search carries the
// resolved ref with ProjectSet.
func TestConversationLeg_ScopeEnforced(t *testing.T) {
	cases := []struct {
		name     string
		resolver ScopeResolver
		wantRef  string
		want     string
	}{
		{"project", resolverFor("proj1"), "proj1", "conversation:a1"},
		{"general", fakeScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindGeneral}}, "", "conversation:g1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := mixedProjectLeg()
			resp, err := convService(t, conv, tc.resolver).Query(context.Background(), RecallWhatRequest{Query: "phrase", K: 1})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if got := convIDs(resp); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("conversation rows = %v, want only %s", got, tc.want)
			}
			if len(conv.calls) != 1 || !conv.calls[0].ProjectSet || conv.calls[0].ProjectRef != tc.wantRef || conv.calls[0].Limit != 1 {
				t.Fatalf("SearchTurns filters = %+v, want one call {ProjectRef:%q ProjectSet:true Limit:1}", conv.calls, tc.wantRef)
			}
		})
	}
}

// flipResolver answers the first Resolve and fails every later one, so
// Query's own resolution passes and the leg's fails.
type flipResolver struct {
	n   atomic.Int32
	err error
}

func (r *flipResolver) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	if r.n.Add(1) == 1 {
		return scope.SessionScope{Kind: scope.ScopeKindSession, Project: "proj1"}, nil
	}
	return scope.SessionScope{}, r.err
}

// TestConversationLeg_ResolutionErrorNoFallback: when the leg's own scope
// resolution fails, the leg is refused whole (Errors["conversation"] set,
// zero conversation rows), the other legs still answer, and SearchTurns is
// never called, scoped or unscoped.
func TestConversationLeg_ResolutionErrorNoFallback(t *testing.T) {
	errResolve := cascade.New(cascade.KindUnavailable, "graph store unreachable")
	files, _, mem := baselineLegs()
	conv := mixedProjectLeg()
	svc, err := NewRecallWhatService(files, conv, mem, rrf.Params{}, testClock(), &flipResolver{err: errResolve}, testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "phrase"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := convIDs(resp); len(got) != 0 {
		t.Fatalf("conversation rows = %v, want none", got)
	}
	if resp.Errors[DomainConversation] != errResolve.Error() {
		t.Fatalf("Errors[conversation] = %q, want %q", resp.Errors[DomainConversation], errResolve.Error())
	}
	if len(resp.Results) != 2 {
		t.Fatalf("Results = %d, want the files and memory rows still answered", len(resp.Results))
	}
	if len(conv.calls) != 0 {
		t.Fatalf("SearchTurns was called %d times after a resolution error: %+v", len(conv.calls), conv.calls)
	}
	out := convService(t, conv, fakeScopeResolver{err: errResolve}).conversationOutcome(context.Background(), RecallWhatRequest{Query: "phrase"}, 10)
	if !out.configured || out.err != errResolve || out.err.Error() != errResolve.Error() || len(out.list.Hits) != 0 || len(conv.calls) != 0 {
		t.Fatalf("leg outcome = %+v (calls %d), want the resolver's own error, no hits, no search", out, len(conv.calls))
	}
}

// TestConversationLeg_ResultPathIsThreadID: a conversation row is
// conversation:<turn_id> in the conversation domain at its thread id.
func TestConversationLeg_ResultPathIsThreadID(t *testing.T) {
	resp, err := convService(t, mixedProjectLeg(), resolverFor("proj1")).Query(context.Background(), RecallWhatRequest{Query: "phrase"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v, want exactly the project A row", resp.Results)
	}
	if r := resp.Results[0]; r.Domain != DomainConversation || r.ID != "conversation:a1" || r.Path != "thA" {
		t.Fatalf("row = %+v, want domain conversation, id conversation:a1, path thA", r)
	}
}

// TestConversationLeg_EgressRefusalDropsCandidate: through the real egress
// engine, restricted and local-only threads are refused and withheld, a
// thread whose tier cannot be read is dropped with Errors["conversation"]
// set, and the public thread's row survives.
func TestConversationLeg_EgressRefusalDropsCandidate(t *testing.T) {
	p := ref("proj1")
	conv := &scopedConvLeg{
		turns: []scopedTurn{turnOn("r1", "thR", p, 1), turnOn("l1", "thL", p, 2), turnOn("e1", "thE", p, 3), turnOn("u1", "thU", p, 4)},
		tiers: map[string]provider.SensitivityTier{"thR": provider.SensitivityRestricted,
			"thL": provider.SensitivityLocalOnly, "thU": provider.SensitivityPublic},
		tierErr: map[string]error{"thE": errors.New("privacy row unreadable")},
	}
	svc := convService(t, conv, resolverFor("proj1"))
	out := svc.conversationOutcome(context.Background(), RecallWhatRequest{Query: "phrase"}, 10)
	if out.err != nil || out.note != errConvTierRead || out.note.Error() != errConvTierRead.Error() || out.withheld != 2 {
		t.Fatalf("outcome err=%v note=%v withheld=%d, want no err, the tier-read note, 2 withheld", out.err, out.note, out.withheld)
	}
	if len(out.list.Hits) != 1 || out.list.Hits[0].ChunkID != "conversation:u1" {
		t.Fatalf("hits = %+v, want only conversation:u1", out.list.Hits)
	}
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "phrase"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := convIDs(resp); len(got) != 1 || got[0] != "conversation:u1" {
		t.Fatalf("conversation rows = %v, want only conversation:u1", got)
	}
	if resp.Errors[DomainConversation] != errConvTierRead.Error() || resp.Withheld != 2 {
		t.Fatalf("Errors=%v Withheld=%d, want the tier-read note and 2 withheld", resp.Errors, resp.Withheld)
	}
}

// TestConversationLeg_UntrustedTurnRankedUntrusted: a turn marked
// untrusted is ranked corpus.TrustUntrustedSource (and so never returned);
// its trusted neighbour is ranked trusted and returned.
func TestConversationLeg_UntrustedTurnRankedUntrusted(t *testing.T) {
	bad := turnOn("x1", "thA", ref("proj1"), 1)
	bad.match.Turn.Untrusted = true
	conv := &scopedConvLeg{turns: []scopedTurn{bad, turnOn("a1", "thA", ref("proj1"), 2)},
		tiers: map[string]provider.SensitivityTier{"thA": provider.SensitivityPublic}}
	svc := convService(t, conv, resolverFor("proj1"))
	out := svc.conversationOutcome(context.Background(), RecallWhatRequest{Query: "phrase"}, 10)
	trust := map[string]corpus.TrustLevel{}
	for _, h := range out.list.Hits {
		trust[h.ChunkID] = h.Trust
	}
	if trust["conversation:x1"] != corpus.TrustUntrustedSource || trust["conversation:a1"] != corpus.TrustTrusted {
		t.Fatalf("trust = %v, want x1 untrusted-source and a1 trusted", trust)
	}
	resp, err := svc.Query(context.Background(), RecallWhatRequest{Query: "phrase"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := convIDs(resp); len(got) != 1 || got[0] != "conversation:a1" || resp.Withheld != 1 {
		t.Fatalf("rows = %v withheld = %d, want only conversation:a1 and the untrusted turn withheld", got, resp.Withheld)
	}
}

// TestConversationOutcome_NilLegNotConfigured: a build with no
// conversation store wired reports "not configured", not an error. Built
// via NewRecallWhatService directly so the interface is genuinely nil
// (a typed-nil *fakeConvLeg would box into a non-nil interface).
func TestConversationOutcome_NilLegNotConfigured(t *testing.T) {
	files, _, mem := baselineLegs()
	svc, err := NewRecallWhatService(files, nil, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	out := svc.conversationOutcome(context.Background(), RecallWhatRequest{Query: "hello", Scope: "proj1"}, 10)
	if out.configured {
		t.Fatal("a nil conversation leg reported configured")
	}
	if out.err != nil {
		t.Fatalf("a nil conversation leg reported an error: %v", out.err)
	}
}
