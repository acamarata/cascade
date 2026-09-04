// Purpose: the namespace/model guard and the vector write path.
//
// The first test in this file is the one that matters most in the
// package. Two embedding models in one namespace produce similarity
// scores that look entirely plausible and mean nothing, and nothing
// downstream can detect it, so the write has to refuse.

package embed

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// faultyVectors wraps a real vector store and fails the Nth Upsert.
type faultyVectors struct {
	provider.VectorStore
	upserts    int
	failOnCall int
}

func (f *faultyVectors) Upsert(ctx context.Context, ns string, vectors []provider.Vector) error {
	f.upserts++
	if f.failOnCall != 0 && f.upserts == f.failOnCall {
		return cascade.New(cascade.KindUnavailable, "vector store is down")
	}
	return f.VectorStore.Upsert(ctx, ns, vectors)
}

func TestUpsertRefusesASecondModelInTheSameNamespace(t *testing.T) {
	store := storetest.NewMemStore()
	vectors := localvector.New(store)
	clock := runtime.NewFixedClock(fixedInstant)
	c := testCorpus("notes")
	ns := fusion.NamespaceFor(c.ID)

	first, err := New(&fakeEmbedder{model: testModel}, vectors, store, clock, 4)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := first.Run(context.Background(), Request{Corpus: c, Chunks: chunks(3)}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	other := &fakeEmbedder{model: provider.EmbedModel{ID: "other-embed-v2", Dimensions: 4}}
	second, err := New(other, vectors, store, clock, 4)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = second.Run(context.Background(), Request{
		Corpus: c,
		Chunks: []retrieval.Chunk{newChunk("new.md", "content the first run never saw")},
	})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict when a second model writes into %q", err, ns)
	}
	if other.embedCalls() != 0 {
		t.Errorf("the refused run still called the second embedder %d times", other.embedCalls())
	}
	n, err := vectors.Count(context.Background(), ns)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("namespace holds %d vectors, want the first model's 3 and nothing from the second", n)
	}
}

func TestUpsertRefusesTheSameModelIDAtADifferentWidth(t *testing.T) {
	store := storetest.NewMemStore()
	vectors := localvector.New(store)
	clock := runtime.NewFixedClock(fixedInstant)
	c := testCorpus("notes")

	first, _ := New(&fakeEmbedder{model: testModel}, vectors, store, clock, 4)
	if _, err := first.Run(context.Background(), Request{Corpus: c, Chunks: chunks(2)}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	widened := provider.EmbedModel{ID: testModel.ID, Dimensions: testModel.Dimensions + 4}
	second, _ := New(&fakeEmbedder{model: widened}, vectors, store, clock, 4)
	_, err := second.Run(context.Background(), Request{
		Corpus: c,
		Chunks: []retrieval.Chunk{newChunk("new.md", "body written under the wider model")},
	})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for the same model id at another width", err)
	}
}

func TestUpsertKeepsCorporaInSeparateNamespaces(t *testing.T) {
	h := newHarness(t, 4)
	body := []retrieval.Chunk{newChunk("a.md", "content in both corpora")}
	for _, id := range []string{"personal", "project"} {
		if _, err := h.pipeline.Run(context.Background(), Request{
			Corpus: testCorpus(id), Chunks: body,
		}); err != nil {
			t.Fatalf("run for %s: %v", id, err)
		}
	}
	for _, id := range []string{"personal", "project"} {
		if got := h.count(t, id); got != 1 {
			t.Errorf("corpus %s holds %d vectors, want its own 1", id, got)
		}
	}
	names, err := h.vectors.Namespaces(context.Background())
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("namespaces = %v, want one per corpus", names)
	}
	// The same content in two corpora is embedded twice on purpose: each
	// corpus needs its own copy, because a namespace is the unit a scope
	// decision translates into.
	if h.embedder.embedCalls() != 2 {
		t.Errorf("embed calls = %d, want one per corpus", h.embedder.embedCalls())
	}
}

func TestUpsertCarriesPathAndModelMetadata(t *testing.T) {
	h := newHarness(t, 4)
	c := testCorpus("notes")
	chunk := newChunk("docs/guide.md", "the only body in this corpus")
	if _, err := h.pipeline.Run(context.Background(), Request{
		Corpus: c, Chunks: []retrieval.Chunk{chunk},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	matches, err := h.vectors.Query(context.Background(), fusion.NamespaceFor(c.ID),
		provider.VectorQuery{Values: vectorFor(string(chunk.Content), testModel.Dimensions), TopK: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].ID != chunk.ID {
		t.Errorf("vector id = %q, want the chunk's stable id %q", matches[0].ID, chunk.ID)
	}
	if got := matches[0].Metadata[fusion.MetadataKeyPath]; got != chunk.Path {
		t.Errorf("path metadata = %v, want %q", got, chunk.Path)
	}
	if got := matches[0].Metadata[MetadataKeyModel]; got != testModel.ID {
		t.Errorf("model metadata = %v, want %q", got, testModel.ID)
	}
}

func TestUpsertReplacesRatherThanDuplicating(t *testing.T) {
	h := newHarness(t, 4)
	c := testCorpus("notes")
	chunk := newChunk("a.md", "one body")
	if _, err := h.pipeline.Run(context.Background(), Request{
		Corpus: c, Chunks: []retrieval.Chunk{chunk},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Clear the ledger so the pipeline re-embeds the same content, which
	// is the state a crash between the vector write and the ledger write
	// leaves behind. The re-write must replace the row, not add one.
	if err := h.store.Delete(context.Background(), ledgerNamespace,
		ledgerKey(fusion.NamespaceFor(c.ID), chunk.ID)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res, err := h.pipeline.Run(context.Background(), Request{
		Corpus: c, Chunks: []retrieval.Chunk{chunk},
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Upserted != 1 {
		t.Errorf("second run upserted %d, want the one replacement", res.Upserted)
	}
	if got := h.count(t, c.ID); got != 1 {
		t.Errorf("namespace holds %d vectors, want 1 after an insert-or-replace", got)
	}
}

func TestRunFailedBatchLeavesEarlierBatchesAndNoPartialBatch(t *testing.T) {
	emb := &fakeEmbedder{model: testModel, failOnCall: 3}
	h := newHarnessWith(t, emb, 1)
	c := testCorpus("notes")
	in := chunks(5)

	res, err := h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: in})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the failing embedder", err)
	}
	if res.Upserted != 2 || res.Embedded != 2 {
		t.Errorf("result = %+v, want the two batches that committed before the fault", res)
	}
	if got := h.count(t, c.ID); got != 2 {
		t.Errorf("namespace holds %d vectors, want exactly the 2 committed batches", got)
	}
	// The failed batch left no ledger entry, so the re-run resumes at it.
	emb.failOnCall = 0
	res, err = h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: in})
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if res.Skipped != 2 || res.Embedded != 3 {
		t.Errorf("resumed run = %+v, want 2 skipped and only the remaining 3 embedded", res)
	}
	if got := h.count(t, c.ID); got != 5 {
		t.Errorf("namespace holds %d vectors after the resume, want all 5", got)
	}
}

func TestRunUpsertFailureLeavesNoVectorsFromThatBatch(t *testing.T) {
	store := storetest.NewMemStore()
	vectors := &faultyVectors{VectorStore: localvector.New(store), failOnCall: 2}
	emb := &fakeEmbedder{model: testModel}
	p, err := New(emb, vectors, store, runtime.NewFixedClock(fixedInstant), 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := testCorpus("notes")
	res, err := p.Run(context.Background(), Request{Corpus: c, Chunks: chunks(4)})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the failing vector store", err)
	}
	if res.Upserted != 1 {
		t.Errorf("result = %+v, want only the first batch counted as written", res)
	}
	n, cerr := vectors.Count(context.Background(), fusion.NamespaceFor(c.ID))
	if cerr != nil {
		t.Fatalf("Count: %v", cerr)
	}
	if n != 1 {
		t.Errorf("namespace holds %d vectors, want only the batch that committed", n)
	}
}

func TestRunRefusesMalformedEmbedderResponses(t *testing.T) {
	wrong := provider.EmbedModel{ID: "sneaky-v9", Dimensions: 4}
	cases := map[string]*fakeEmbedder{
		"foreign model":  {model: testModel, wrongModel: &wrong},
		"short vector":   {model: testModel, shortVector: true},
		"dropped output": {model: testModel, dropOutput: true},
	}
	for name, emb := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarnessWith(t, emb, 4)
			c := testCorpus("notes")
			_, err := h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: chunks(2)})
			if !cascade.HasKind(err, cascade.KindIntegrity) {
				t.Fatalf("err = %v, want KindIntegrity", err)
			}
			if got := h.count(t, c.ID); got != 0 {
				t.Errorf("namespace holds %d vectors after a refused batch, want 0", got)
			}
		})
	}
}

func TestBuildVectorsRefusesMismatchedLengths(t *testing.T) {
	_, err := buildVectors(chunks(2), nil, testModel)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity when the counts disagree", err)
	}
}
