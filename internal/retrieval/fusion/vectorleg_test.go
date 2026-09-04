package fusion

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeEmbedder returns a fixed vector. It stands in for the recorded
// real-embedder fixture until that fixture's ticket produces one: what
// this leg does with a vector depends on the vector's shape and not on its
// values, so the recorded fixture changes nothing asserted here.
type fakeEmbedder struct {
	values [][]float32
	err    error
	calls  int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.values != nil {
		return f.values, nil
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

// fakeVectorStore records which namespaces were opened, which is the
// property the scope binding is asserted through.
type fakeVectorStore struct {
	byNamespace map[string][]provider.VectorMatch
	opened      []string
	err         error
}

func (f *fakeVectorStore) Query(_ context.Context, namespace string, _ provider.VectorQuery) ([]provider.VectorMatch, error) {
	f.opened = append(f.opened, namespace)
	if f.err != nil {
		return nil, f.err
	}
	return f.byNamespace[namespace], nil
}

func (f *fakeVectorStore) Upsert(context.Context, string, []provider.Vector) error { return nil }
func (f *fakeVectorStore) Delete(context.Context, string, []string) error          { return nil }
func (f *fakeVectorStore) Count(context.Context, string) (int, error)              { return 0, nil }
func (f *fakeVectorStore) Namespaces(context.Context) ([]string, error) {
	return nil, errors.New("a leg must never enumerate every namespace: it opens the scope-bound ones")
}

type fakeSink struct {
	published []events.Event
	err       error
}

func (f *fakeSink) Publish(_ context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	if f.err != nil {
		return events.Event{}, f.err
	}
	_ = namespace
	ev := events.Event{Kind: kind, Source: source, Payload: payload}
	f.published = append(f.published, ev)
	return ev, nil
}

func project1Filter(t *testing.T) (*ScopeFilter, scenario) {
	t.Helper()
	store, p1, _ := leakStore(t)
	f, err := NewScopeFilter(store, corpus.Query{
		Membership:  p1.Membership,
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	return f, p1
}

// TestVectorLeg_SkipsWithoutEmbedder is the degrade path: no embedder is a
// supported local configuration, so the leg reports that it did not run
// and the query proceeds on the full-text leg alone.
func TestVectorLeg_SkipsWithoutEmbedder(t *testing.T) {
	filter, _ := project1Filter(t)
	sink := &fakeSink{}
	store := &fakeVectorStore{}

	leg := NewVectorLeg(nil, store, sink)
	list, ran, err := leg.Query(context.Background(), filter, "scope filter", 10)
	if err != nil || ran {
		t.Fatalf("a missing embedder must be a clean skip, got ran=%t err=%v", ran, err)
	}
	if len(list.Hits) != 0 {
		t.Errorf("the skipped leg produced %d hits, which would be invented vectors", len(list.Hits))
	}
	if len(store.opened) != 0 {
		t.Errorf("the skipped leg still opened namespaces %v", store.opened)
	}
	if len(sink.published) != 1 {
		t.Fatalf("published %d events, want the one unavailability event", len(sink.published))
	}
	if sink.published[0].Kind != EventKindVectorLegUnavailable {
		t.Errorf("event kind = %q, want %q", sink.published[0].Kind, EventKindVectorLegUnavailable)
	}
}

// TestVectorLeg_SkipVariants covers the other unconfigured shapes: no
// vector store, and no event sink to report the skip through.
func TestVectorLeg_SkipVariants(t *testing.T) {
	filter, _ := project1Filter(t)
	sink := &fakeSink{}
	if _, ran, err := NewVectorLeg(&fakeEmbedder{}, nil, sink).
		Query(context.Background(), filter, "q", 5); err != nil || ran {
		t.Fatalf("no vector store: want a clean skip, got ran=%t err=%v", ran, err)
	}
	if len(sink.published) != 1 {
		t.Errorf("published %d events, want one", len(sink.published))
	}
	if _, ran, err := NewVectorLeg(nil, nil, nil).
		Query(context.Background(), filter, "q", 5); err != nil || ran {
		t.Fatalf("no sink: want a clean skip, got ran=%t err=%v", ran, err)
	}
}

// TestVectorLeg_QueriesOnlyScopeBoundNamespaces is the scope binding in
// the leg: it opens the namespaces the filter authorized and no others,
// and never asks the driver what namespaces exist.
func TestVectorLeg_QueriesOnlyScopeBoundNamespaces(t *testing.T) {
	store, p1, p3 := leakStore(t)
	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  p1.Membership,
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	vec := &fakeVectorStore{byNamespace: map[string][]provider.VectorMatch{
		NamespaceFor(p1.Corpus.ID): {
			{ID: p1.Records[0].ID, Score: 0.9, Metadata: map[string]any{MetadataKeyPath: "docs/retrieval.md"}},
			{ID: p1.Records[2].ID, Score: 0.4},
		},
		NamespaceFor(p3.Corpus.ID): {
			{ID: p3.Records[0].ID, Score: 0.99},
		},
	}}

	list, ran, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).
		Query(context.Background(), filter, "scope filter", 10)
	if err != nil || !ran {
		t.Fatalf("want the leg to run, got ran=%t err=%v", ran, err)
	}
	if len(vec.opened) != 1 || vec.opened[0] != NamespaceFor(p1.Corpus.ID) {
		t.Errorf("opened %v, want only Project 1's namespace", vec.opened)
	}
	for _, h := range list.Hits {
		if h.CorpusID != p1.Corpus.ID {
			t.Errorf("hit %s came from corpus %s", h.ChunkID, h.CorpusID)
		}
	}
	if list.Strategy != rrf.StrategyVector || list.Weight != rrf.NeutralWeight {
		t.Errorf("list = %+v, want the vector leg at neutral weight", list)
	}
	if len(list.Hits) != 2 {
		t.Fatalf("got %d hits, want both authorized matches", len(list.Hits))
	}
	if list.Hits[0].Path != "docs/retrieval.md" || list.Hits[1].Path != "" {
		t.Errorf("paths = %q and %q, want the stored one and no invented one",
			list.Hits[0].Path, list.Hits[1].Path)
	}
	if list.Hits[1].Trust != corpus.TrustUntrustedSource {
		t.Errorf("trust = %q, want the fixture's untrusted record tag carried through", list.Hits[1].Trust)
	}
}

// TestVectorLeg_WithholdsUnclassifiedMatches covers the case the namespace
// binding alone cannot: a driver returning an id inside an authorized
// namespace that the corpus model withheld at the record level.
func TestVectorLeg_WithholdsUnclassifiedMatches(t *testing.T) {
	filter, p1 := project1Filter(t)
	vec := &fakeVectorStore{byNamespace: map[string][]provider.VectorMatch{
		NamespaceFor(p1.Corpus.ID): {
			{ID: "a-record-the-model-withheld", Score: 0.99},
			{ID: p1.Records[0].ID, Score: 0.1},
		},
	}}
	list, ran, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).
		Query(context.Background(), filter, "q", 10)
	if err != nil || !ran {
		t.Fatalf("want the leg to run, got ran=%t err=%v", ran, err)
	}
	if len(list.Hits) != 1 || list.Hits[0].ChunkID != p1.Records[0].ID {
		t.Errorf("hits = %+v, want only the record with a resolved classification", list.Hits)
	}
}

func TestVectorLeg_TopKCapsTheMergedList(t *testing.T) {
	filter, p1 := project1Filter(t)
	matches := make([]provider.VectorMatch, 0, len(p1.Records))
	for i, r := range p1.Records {
		matches = append(matches, provider.VectorMatch{ID: r.ID, Score: float32(len(p1.Records) - i)})
	}
	vec := &fakeVectorStore{byNamespace: map[string][]provider.VectorMatch{
		NamespaceFor(p1.Corpus.ID): matches,
	}}
	list, _, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).
		Query(context.Background(), filter, "q", 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(list.Hits) != 2 {
		t.Errorf("got %d hits, want the requested cap of 2", len(list.Hits))
	}
	if list.Hits[0].ChunkID != p1.Records[0].ID {
		t.Errorf("top hit = %s, want the highest-scoring match", list.Hits[0].ChunkID)
	}
}

func TestVectorLeg_EmptyScopeRunsWithoutOpeningAnything(t *testing.T) {
	store, _, _ := leakStore(t)
	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  corpus.Membership{Scope: "project:unrelated"},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	vec := &fakeVectorStore{}
	embedder := &fakeEmbedder{}
	list, ran, err := NewVectorLeg(embedder, vec, nil).Query(context.Background(), filter, "q", 10)
	if err != nil || !ran {
		t.Fatalf("want the leg to run and find nothing, got ran=%t err=%v", ran, err)
	}
	if len(list.Hits) != 0 {
		t.Errorf("an unauthorized session got %d hits", len(list.Hits))
	}
	if embedder.calls != 0 {
		t.Error("the query was embedded for a session authorized to read nothing")
	}
}

func TestVectorLeg_ErrorPaths(t *testing.T) {
	filter, _ := project1Filter(t)
	ctx := context.Background()

	t.Run("nil filter", func(t *testing.T) {
		_, _, err := NewVectorLeg(&fakeEmbedder{}, &fakeVectorStore{}, nil).Query(ctx, nil, "q", 10)
		assertKind(t, err, cascade.KindInvalidInput)
	})
	t.Run("non-positive topK", func(t *testing.T) {
		_, _, err := NewVectorLeg(&fakeEmbedder{}, &fakeVectorStore{}, nil).Query(ctx, filter, "q", 0)
		assertKind(t, err, cascade.KindInvalidInput)
	})
	t.Run("embedder failure", func(t *testing.T) {
		e := &fakeEmbedder{err: errors.New("provider down")}
		_, _, err := NewVectorLeg(e, &fakeVectorStore{}, nil).Query(ctx, filter, "q", 10)
		assertKind(t, err, cascade.KindUnavailable)
	})
	t.Run("embedder returned the wrong shape", func(t *testing.T) {
		e := &fakeEmbedder{values: [][]float32{}}
		_, _, err := NewVectorLeg(e, &fakeVectorStore{}, nil).Query(ctx, filter, "q", 10)
		assertKind(t, err, cascade.KindIntegrity)
	})
	t.Run("driver failure", func(t *testing.T) {
		vec := &fakeVectorStore{err: errors.New("index unreadable")}
		_, _, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).Query(ctx, filter, "q", 10)
		assertKind(t, err, cascade.KindUnavailable)
	})
	t.Run("event log failure surfaces", func(t *testing.T) {
		sink := &fakeSink{err: cascade.New(cascade.KindUnavailable, "log unwritable")}
		_, ran, err := NewVectorLeg(nil, nil, sink).Query(ctx, filter, "q", 10)
		if ran {
			t.Error("the leg claimed to run")
		}
		assertKind(t, err, cascade.KindUnavailable)
	})
}

func assertKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal, got none")
	}
	got, ok := cascade.KindOf(err)
	if !ok || got != want {
		t.Errorf("error kind = %v (taxonomy %t), want %v: %v", got, ok, want, err)
	}
}
