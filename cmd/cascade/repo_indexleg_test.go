package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// countingEmbedder returns a fixed 2-dim vector and counts every call.
type countingEmbedder struct{ calls, inputs int }

func (e *countingEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "count-test", Dimensions: 2}
}

func (e *countingEmbedder) Embed(_ context.Context, in []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	e.calls++
	e.inputs += len(in)
	out := make([]provider.EmbedOutput, len(in))
	for i := range in {
		out[i] = provider.EmbedOutput{Vector: []float32{1, 0}, Model: e.Model()}
	}
	return out, nil
}

// recordingVectors records Upsert and Delete ids per namespace.
type recordingVectors struct{ upserted, deleted map[string][]string }

func newRecordingVectors() *recordingVectors {
	return &recordingVectors{upserted: map[string][]string{}, deleted: map[string][]string{}}
}

func (v *recordingVectors) Upsert(_ context.Context, ns string, vs []provider.Vector) error {
	for _, x := range vs {
		v.upserted[ns] = append(v.upserted[ns], x.ID)
	}
	return nil
}

func (v *recordingVectors) Delete(_ context.Context, ns string, ids []string) error {
	v.deleted[ns] = append(v.deleted[ns], ids...)
	return nil
}

func (v *recordingVectors) Query(context.Context, string, provider.VectorQuery) ([]provider.VectorMatch, error) {
	return nil, nil
}

func (v *recordingVectors) Count(_ context.Context, ns string) (int, error) {
	return len(v.upserted[ns]), nil
}

func (v *recordingVectors) Namespaces(context.Context) ([]string, error) { return nil, nil }

type indexLegFixture struct {
	leg     *repoIndexLeg
	idx     *retrieval.Index
	emb     *countingEmbedder
	vectors *recordingVectors
}

func newIndexLegFixture(t *testing.T, sel func(provider.Embedder) *embedsel.Selection) indexLegFixture {
	t.Helper()
	idx := newMemIndex(t)
	emb, vec := &countingEmbedder{}, newRecordingVectors()
	clock := runtime.NewFixedClock(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	leg, err := newRepoIndexLeg(idx, sel(emb), vec, storetest.NewMemStore(), clock)
	if err != nil {
		t.Fatal(err)
	}
	return indexLegFixture{leg: leg, idx: idx, emb: emb, vectors: vec}
}

func newMemIndex(t *testing.T) *retrieval.Index {
	t.Helper()
	idx, err := retrieval.NewIndex(storetest.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func admitting(tiers ...provider.SensitivityTier) func(provider.Embedder) *embedsel.Selection {
	return func(e provider.Embedder) *embedsel.Selection {
		return embedsel.New(e, "test", "count-test", 2, tiers)
	}
}

const zebraSrc = "package a\n\n// Zebra holds the zebraquux marker.\nfunc Zebra() string { return \"zebraquux\" }\n"

func expectedChunkIDs(t *testing.T, rel string, content []byte) []string {
	t.Helper()
	ch, err := retrieval.ChunkerFor(rel)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := ch.Chunk(rel, content)
	if err != nil {
		t.Fatal(err)
	}
	return idsOf(chunks)
}

func idsOf(chunks []retrieval.Chunk) []string {
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	return ids
}

func searchHits(t *testing.T, idx *retrieval.Index, text, corpusID string, ids []string) []retrieval.Hit {
	t.Helper()
	sorted := slices.Sorted(slices.Values(ids))
	hits, err := idx.Search(context.Background(), retrieval.SearchRequest{
		Text:  text,
		Scope: fusion.ScopePredicate{CorpusIDs: []string{corpusID}, RecordIDs: sorted},
	}, 10)
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	return hits
}

func TestIndexLegIsIndexLeg(t *testing.T) {
	var _ repo.IndexLeg = (*repoIndexLeg)(nil)
	idx := newMemIndex(t)
	sel := embedsel.New(&countingEmbedder{}, "test", "count-test", 2, nil)
	vec, meta := newRecordingVectors(), storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	cases := map[string]func() (*repoIndexLeg, error){
		"idx":     func() (*repoIndexLeg, error) { return newRepoIndexLeg(nil, sel, vec, meta, clock) },
		"sel":     func() (*repoIndexLeg, error) { return newRepoIndexLeg(idx, nil, vec, meta, clock) },
		"vectors": func() (*repoIndexLeg, error) { return newRepoIndexLeg(idx, sel, nil, meta, clock) },
		"meta":    func() (*repoIndexLeg, error) { return newRepoIndexLeg(idx, sel, vec, nil, clock) },
		"clock":   func() (*repoIndexLeg, error) { return newRepoIndexLeg(idx, sel, vec, meta, nil) },
	}
	for name, build := range cases {
		leg, err := build()
		if leg != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("nil %s: got (%v, %v), want KindInvalidInput", name, leg, err)
		}
	}
}

func TestIndexLegWriteFTS(t *testing.T) {
	f := newIndexLegFixture(t, admitting())
	ids, err := f.leg.WriteFTS(context.Background(), "r1", "a.go", []byte(zebraSrc))
	if err != nil {
		t.Fatal(err)
	}
	want := expectedChunkIDs(t, "a.go", []byte(zebraSrc))
	if len(ids) == 0 || !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want non-empty %v", ids, want)
	}
	hits := searchHits(t, f.idx, "zebraquux", corpus.CorpusIDCode, ids)
	if len(hits) == 0 || hits[0].Path != "a.go" {
		t.Fatalf("hits = %+v, want a hit on a.go", hits)
	}
}

func TestIndexLegDeleteChunks(t *testing.T) {
	ctx := context.Background()
	f := newIndexLegFixture(t, admitting(provider.SensitivityInternal))
	ids, err := f.leg.WriteFTS(ctx, "r1", "a.go", []byte(zebraSrc))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := f.leg.WriteVector(ctx, "r1", "a.go", ids, []byte(zebraSrc)); pending || err != nil {
		t.Fatalf("WriteVector = (%v, %v), want (false, nil)", pending, err)
	}
	if err := f.leg.DeleteChunks(ctx, "r1", ids); err != nil {
		t.Fatal(err)
	}
	if hits := searchHits(t, f.idx, "zebraquux", corpus.CorpusIDCode, ids); len(hits) != 0 {
		t.Fatalf("hits after delete = %+v, want none", hits)
	}
	ns := fusion.NamespaceFor(corpus.CorpusIDCode)
	if got := f.vectors.deleted[ns]; !slices.Equal(got, ids) {
		t.Fatalf("vector deletes in %s = %v, want %v", ns, got, ids)
	}
	if len(f.vectors.deleted) != 1 {
		t.Fatalf("deletes touched namespaces %v, want only %s", f.vectors.deleted, ns)
	}
	if err := f.leg.DeleteChunks(ctx, "r1", []string{"absent-chunk-id"}); err != nil {
		t.Fatalf("absent ids: %v", err)
	}
}

func TestRepoIndexLegWriteVectorPending(t *testing.T) {
	sels := map[string]func(provider.Embedder) *embedsel.Selection{
		"not-configured": func(provider.Embedder) *embedsel.Selection {
			return embedsel.NotConfigured(errors.New("no embedder"))
		},
		"failed": func(provider.Embedder) *embedsel.Selection {
			return embedsel.Failed("local", errors.New("sidecar down"))
		},
		"public-only": admitting(provider.SensitivityPublic),
	}
	for name, sel := range sels {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newIndexLegFixture(t, sel)
			ids, err := f.leg.WriteFTS(ctx, "r1", "a.go", []byte(zebraSrc))
			if err != nil {
				t.Fatal(err)
			}
			pending, err := f.leg.WriteVector(ctx, "r1", "a.go", ids, []byte(zebraSrc))
			if !pending || err != nil {
				t.Fatalf("WriteVector = (%v, %v), want (true, nil)", pending, err)
			}
			if f.emb.calls != 0 {
				t.Fatalf("embedder calls = %d, want 0", f.emb.calls)
			}
			if len(f.vectors.upserted) != 0 {
				t.Fatalf("upserts = %v, want none", f.vectors.upserted)
			}
		})
	}
}

func TestIndexLegVectorEmbeds(t *testing.T) {
	ctx := context.Background()
	f := newIndexLegFixture(t, admitting(provider.SensitivityInternal))
	ids, err := f.leg.WriteFTS(ctx, "r1", "a.go", []byte(zebraSrc))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.leg.WriteVector(ctx, "r1", "a.go", ids, []byte(zebraSrc))
	if pending || err != nil {
		t.Fatalf("WriteVector = (%v, %v), want (false, nil)", pending, err)
	}
	if f.emb.calls < 1 || f.emb.inputs != len(ids) {
		t.Fatalf("embedder calls=%d inputs=%d, want >=1 and %d", f.emb.calls, f.emb.inputs, len(ids))
	}
	got := f.vectors.upserted[fusion.NamespaceFor(corpus.CorpusIDCode)]
	if !slices.Equal(got, ids) {
		t.Fatalf("upserted ids = %v, want %v", got, ids)
	}
}

func symbolGraph(name string) repo.SymbolGraph {
	return repo.SymbolGraph{Nodes: []repo.GraphNode{{
		ID: "example.com/a#" + name, Kind: repo.NodeType, Name: name,
		Package: "example.com/a", File: "a.go", Line: 3,
	}}}
}

func graphChunkIDs(t *testing.T, g repo.SymbolGraph) []string {
	t.Helper()
	_, chunks, err := retrieval.IngestSymbolGraph(&g, corpus.ScopeRef("r1"), corpus.TrustUntrustedSource)
	if err != nil {
		t.Fatal(err)
	}
	return idsOf(chunks)
}

func TestIndexLegGraphReplaces(t *testing.T) {
	ctx := context.Background()
	f := newIndexLegFixture(t, admitting())
	g1, g2 := symbolGraph("Alpha"), symbolGraph("Beta")
	scope := append(graphChunkIDs(t, g1), graphChunkIDs(t, g2)...)
	if err := f.leg.WriteGraph(ctx, "r1", g1); err != nil {
		t.Fatal(err)
	}
	if hits := searchHits(t, f.idx, "alpha", corpus.CorpusIDGraph, scope); len(hits) == 0 {
		t.Fatal("Alpha not indexed after g1")
	}
	if err := f.leg.WriteGraph(ctx, "r1", g2); err != nil {
		t.Fatal(err)
	}
	assertGraph := func(stage string) {
		t.Helper()
		if hits := searchHits(t, f.idx, "beta", corpus.CorpusIDGraph, scope); len(hits) == 0 {
			t.Fatalf("%s: Beta missing", stage)
		}
		if hits := searchHits(t, f.idx, "alpha", corpus.CorpusIDGraph, scope); len(hits) != 0 {
			t.Fatalf("%s: Alpha still indexed: %+v", stage, hits)
		}
	}
	assertGraph("after g2")
	bad := repo.SymbolGraph{Nodes: []repo.GraphNode{{ID: "x", Kind: "bogus", Package: "p"}}}
	if err := f.leg.WriteGraph(ctx, "r1", bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("invalid graph: err = %v, want KindInvalidInput", err)
	}
	assertGraph("after invalid graph")
}
