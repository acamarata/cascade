package fusion

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
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
	tier   provider.SensitivityTier
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.tier, _ = conductor.DoorSensitivityFrom(ctx)
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

func (f *fakeSink) Publish(_ context.Context, _ string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	if f.err != nil {
		return events.Event{}, f.err
	}
	ev := events.Event{Kind: kind, Source: source, Payload: payload}
	f.published = append(f.published, ev)
	return ev, nil
}

func project1Filter(t *testing.T) (*ScopeFilter, scenario) {
	t.Helper()
	store, p1, _ := leakStore(t)
	f, err := NewScopeFilter(store, corpus.Query{Membership: p1.Membership, Entitlement: corpus.PrivacyProject})
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
	sink, store := &fakeSink{}, &fakeVectorStore{}
	list, ran, err := NewVectorLeg(nil, store, sink).Query(context.Background(), filter, "scope filter", 10)
	if err != nil || ran {
		t.Fatalf("a missing embedder must be a clean skip, got ran=%t err=%v", ran, err)
	}
	if len(list.Hits) != 0 || len(store.opened) != 0 {
		t.Errorf("the skipped leg produced %d hits (invented vectors) and opened %v", len(list.Hits), store.opened)
	}
	if len(sink.published) != 1 || sink.published[0].Kind != EventKindVectorLegUnavailable {
		t.Fatalf("published %+v, want the one %q event", sink.published, EventKindVectorLegUnavailable)
	}
}

// TestVectorLeg_SkipVariants covers the other unconfigured shapes: no
// vector store, and no event sink to report the skip through.
func TestVectorLeg_SkipVariants(t *testing.T) {
	filter, _ := project1Filter(t)
	sink := &fakeSink{}
	if _, ran, err := NewVectorLeg(&fakeEmbedder{}, nil, sink).Query(context.Background(), filter, "q", 5); err != nil || ran {
		t.Fatalf("no vector store: want a clean skip, got ran=%t err=%v", ran, err)
	}
	if len(sink.published) != 1 {
		t.Errorf("published %d events, want one", len(sink.published))
	}
	if _, ran, err := NewVectorLeg(nil, nil, nil).Query(context.Background(), filter, "q", 5); err != nil || ran {
		t.Fatalf("no sink: want a clean skip, got ran=%t err=%v", ran, err)
	}
}

// TestVectorLeg_QueriesOnlyScopeBoundNamespaces is the scope binding in
// the leg: it opens the namespaces the filter authorized and no others,
// and never asks the driver what namespaces exist.
func TestVectorLeg_QueriesOnlyScopeBoundNamespaces(t *testing.T) {
	filter, p1 := project1Filter(t)
	p3 := loadScenario(t, "corpus_project3.json")
	vec := &fakeVectorStore{byNamespace: map[string][]provider.VectorMatch{
		NamespaceFor(p1.Corpus.ID): {
			{ID: p1.Records[0].ID, Score: 0.9, Metadata: map[string]any{MetadataKeyPath: "docs/retrieval.md"}},
			{ID: p1.Records[2].ID, Score: 0.4},
		},
		NamespaceFor(p3.Corpus.ID): {
			{ID: p3.Records[0].ID, Score: 0.99},
		},
	}}

	list, ran, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).Query(context.Background(), filter, "scope filter", 10)
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
	list, ran, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).Query(context.Background(), filter, "q", 10)
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
	list, _, err := NewVectorLeg(&fakeEmbedder{}, vec, nil).Query(context.Background(), filter, "q", 2)
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
	filter, err := NewScopeFilter(store, corpus.Query{Membership: corpus.Membership{Scope: "project:unrelated"}, Entitlement: corpus.PrivacyProject})
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
	logDown := &fakeSink{err: cascade.New(cascade.KindUnavailable, "log unwritable")}
	cases := []struct {
		name   string
		leg    *VectorLeg
		filter *ScopeFilter
		topK   int
		want   cascade.Kind
	}{
		{"nil filter", NewVectorLeg(&fakeEmbedder{}, &fakeVectorStore{}, nil), nil, 10, cascade.KindInvalidInput},
		{"non-positive topK", NewVectorLeg(&fakeEmbedder{}, &fakeVectorStore{}, nil), filter, 0, cascade.KindInvalidInput},
		{"embedder failure", NewVectorLeg(&fakeEmbedder{err: errors.New("provider down")}, &fakeVectorStore{}, nil), filter, 10, cascade.KindUnavailable},
		{"embedder returned the wrong shape", NewVectorLeg(&fakeEmbedder{values: [][]float32{}}, &fakeVectorStore{}, nil), filter, 10, cascade.KindIntegrity},
		{"driver failure", NewVectorLeg(&fakeEmbedder{}, &fakeVectorStore{err: errors.New("index unreadable")}, nil), filter, 10, cascade.KindUnavailable},
		{"event log failure surfaces", NewVectorLeg(nil, nil, logDown), filter, 10, cascade.KindUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ran, err := tc.leg.Query(context.Background(), tc.filter, "q", tc.topK); ran {
				t.Error("the leg claimed to run")
			} else {
				assertKind(t, err, tc.want)
			}
		})
	}
}

// TestVectorLegSkipsPersonalScopeForExternalEmbedder: a scope holding a
// personal record is never embedded by an embedder that is not local, the
// skip is recorded with its reason, and an admitted scope's query carries
// the scope's own tier on the context.
func TestVectorLegSkipsPersonalScopeForExternalEmbedder(t *testing.T) {
	store, p1, _ := leakStore(t)
	personal := p1.Records[0]
	personal.ID, personal.Privacy = "project1/notes/diary.md#entry", corpus.PrivacyPersonal
	if err := store.AddRecord(personal); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	filter, err := NewScopeFilter(store, corpus.Query{Membership: p1.Membership, Entitlement: corpus.PrivacyPersonal})
	if err != nil || !filter.HasPersonal() {
		t.Fatalf("NewScopeFilter: %v (personal in scope %t)", err, err == nil && filter.HasPersonal())
	}
	sink, e, vec := &fakeSink{}, &fakeEmbedder{}, &fakeVectorStore{}
	if _, ran, err := NewVectorLegWith(e, vec, sink, VectorLegOptions{}).Query(context.Background(), filter, "q", 5); ran || err != nil {
		t.Fatalf("personal scope, external embedder: ran=%t err=%v, want a clean skip", ran, err)
	}
	if e.calls != 0 || len(vec.opened) != 0 || len(sink.published) != 1 ||
		string(sink.published[0].Payload) != `{"reason":"`+ReasonPersonalScope+`"}` {
		t.Fatalf("embeds %d, opened %v, events %+v: want no embed and one reasoned event", e.calls, vec.opened, sink.published)
	}
	if _, ran, err := NewVectorLegWith(e, vec, nil, VectorLegOptions{AdmitPersonal: true}).Query(context.Background(), filter, "q", 5); !ran || err != nil || e.tier != provider.SensitivityLocalOnly {
		t.Fatalf("local embedder: ran=%t err=%v tier %s, want a run stamped local-only", ran, err, e.tier)
	}
	project, _ := project1Filter(t)
	if _, ran, err := NewVectorLeg(e, vec, nil).Query(context.Background(), project, "q", 5); !ran || err != nil || e.tier != provider.SensitivityInternal {
		t.Fatalf("project scope: ran=%t err=%v tier %s, want a run stamped internal", ran, err, e.tier)
	}
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
