package conversation

// Purpose: the query path against the REAL recall.what service
//   (internal/retrieval.RecallWhatService behind its real handler on a real
//   *rpc.Registry), never an injected top-level error: when the
//   conversation leg reports itself unavailable, --resume-query is
//   KindUnavailable even though the file leg answered, and a service with
//   no conversation leg at all yields KindNotFound -- in neither case is a
//   file row composed. Only the file leg, scope resolver and vault are test
//   doubles (retrieval's own recallwhat_testkit_test.go shapes); the
//   conversation leg adapts this package's real sqlite store.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recallFilesLeg answers one trusted, cited file hit.
type recallFilesLeg struct{}

func (recallFilesLeg) Query(context.Context, recall.Request) (recall.Response, error) {
	return recall.Response{
		Results:   []recall.Result{{ChunkID: "f1", Path: "plan.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 0.9}},
		Citations: []citations.Citation{{ChunkID: "f1", Path: "plan.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Rank: 1, Score: 0.9}},
	}, nil
}

// recallConvLeg adapts this package's real Store to recall.what's
// conversation-leg shape (the composition root's adapter does the same);
// a non-nil unavailable is returned by every search instead.
type recallConvLeg struct {
	store       Store
	unavailable error
}

func (l recallConvLeg) SearchTurns(ctx context.Context, q string, f retrieval.RecallWhatSearchFilter) ([]retrieval.RecallWhatTurnMatch, error) {
	if l.unavailable != nil {
		return nil, l.unavailable
	}
	hits, err := l.store.SearchTurns(ctx, q, SearchFilter{ThreadID: f.ThreadID, Limit: f.Limit, ProjectRef: f.ProjectRef, ProjectSet: f.ProjectSet})
	out := make([]retrieval.RecallWhatTurnMatch, 0, len(hits))
	for _, h := range hits {
		out = append(out, retrieval.RecallWhatTurnMatch{Turn: retrieval.RecallWhatTurn{ID: h.Turn.ID, ThreadID: h.Turn.ThreadID, Role: string(h.Turn.Role)}, Rank: h.Rank})
	}
	return out, err
}
func (l recallConvLeg) ListSegments(ctx context.Context, turnID string) ([]retrieval.RecallWhatSegment, error) {
	segs, err := l.store.ListSegments(ctx, turnID)
	out := make([]retrieval.RecallWhatSegment, 0, len(segs))
	for _, s := range segs {
		out = append(out, retrieval.RecallWhatSegment{Content: s.Content})
	}
	return out, err
}
func (l recallConvLeg) ThreadPrivacy(ctx context.Context, id string) (provider.SensitivityTier, error) {
	return l.store.ThreadPrivacy(ctx, id)
}

type recallScope struct{}

func (recallScope) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	return scope.SessionScope{Kind: scope.ScopeKindSession, Project: "proj1"}, nil
}

type recallEmptyVault struct{}

func (recallEmptyVault) List(context.Context) ([]string, error) { return nil, nil }
func (recallEmptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "empty test vault")
}

// realRecallWhatRegistry registers a real RecallWhatService on a registry.
// conv is the conversation leg (nil: none configured).
func realRecallWhatRegistry(t *testing.T, conv retrieval.RecallWhatConversationLeg) *rpc.Registry {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), recallEmptyVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, err := retrieval.NewRecallWhatService(recallFilesLeg{}, conv, nil, rrf.Params{}, clock, recallScope{}, engine)
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	registry := rpc.NewRegistry()
	retrieval.NewRecallWhatHandler(svc).Register(registry)
	return registry
}

// TestResumeQueryUnavailableUntilConversationLeg (contract name). The
// conversation leg's store reports itself unavailable; the precondition
// dispatch proves the real service answered a file row AND reported
// Errors["conversation"]; Resume must then be KindUnavailable, not a block
// built from the file row. Failing input: ignoring Errors["conversation"]
// (the query then reports KindNotFound instead).
func TestResumeQueryUnavailableUntilConversationLeg(t *testing.T) {
	store := newResumeStore(t)
	down := cascade.New(cascade.KindUnavailable, "conversation: search index unavailable")
	registry := realRecallWhatRegistry(t, recallConvLeg{store: store, unavailable: down})
	raw, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "recall.what", Params: json.RawMessage(`{"query":"plan"}`)})
	if errObj != nil {
		t.Fatalf("recall.what: %+v", errObj)
	}
	resp, err := decodeRecallWhatResponse(raw)
	if err != nil || len(resp.Results) == 0 || resp.Results[0].Domain != "file" || resp.Errors["conversation"] == "" {
		t.Fatalf("precondition: recall.what = %+v (%v), want a file row and Errors[conversation]", resp, err)
	}

	r, err := NewResumer(store, echoComposer{}, registry, &recordingSubst{})
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{Query: "plan", IntoTier: provider.SensitivityLocalOnly})
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "conversation domain is unavailable") {
		t.Fatalf("Resume = %+v, %v; want KindUnavailable for the conversation leg", res, err)
	}
}

// TestResumeQuery_NoConversationLeg_NotFound: a real service with no
// conversation leg answers only the file row, and the query path refuses
// with KindNotFound instead of composing it.
func TestResumeQuery_NoConversationLeg_NotFound(t *testing.T) {
	store := newResumeStore(t)
	r, err := NewResumer(store, echoComposer{}, realRecallWhatRegistry(t, nil), &recordingSubst{})
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{Query: "plan", IntoTier: provider.SensitivityLocalOnly})
	if !cascade.HasKind(err, cascade.KindNotFound) || res.Context != "" {
		t.Fatalf("Resume = %+v, %v; want KindNotFound and no block", res, err)
	}
}
