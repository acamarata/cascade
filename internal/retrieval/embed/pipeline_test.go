// Purpose: the pipeline's own suite: batching, dedupe as the caller sees
// it, the no-op empty run, and the input refusals that happen before any
// embedding is paid for.
//
// The shared fixtures, including the Embedder double, live in
// fixtures_test.go.

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

func TestRunEmbedsInBatchesAndUpserts(t *testing.T) {
	h := newHarness(t, 2)
	c := testCorpus("notes")
	res, err := h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: chunks(5)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Namespace != fusion.NamespaceFor("notes") {
		t.Errorf("namespace = %q, want the fusion convention %q",
			res.Namespace, fusion.NamespaceFor("notes"))
	}
	if res.Embedded != 5 || res.Upserted != 5 || res.Skipped != 0 {
		t.Errorf("result = %+v, want 5 embedded, 5 upserted, 0 skipped", res)
	}
	if res.Batches != 3 || h.embedder.embedCalls() != 3 {
		t.Errorf("batches = %d, embed calls = %d, want 3 grouped calls for 5 chunks at size 2",
			res.Batches, h.embedder.embedCalls())
	}
	for i, call := range h.embedder.calls {
		if len(call) == 0 {
			t.Errorf("embed call %d was empty", i)
		}
	}
	if got := h.count(t, "notes"); got != 5 {
		t.Errorf("namespace holds %d vectors, want 5", got)
	}
}

func TestRunIsIdempotentOverUnchangedChunks(t *testing.T) {
	h := newHarness(t, 2)
	c := testCorpus("notes")
	in := chunks(5)
	if _, err := h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: in}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	callsAfterFirst := h.embedder.embedCalls()

	res, err := h.pipeline.Run(context.Background(), Request{Corpus: c, Chunks: in})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if h.embedder.embedCalls() != callsAfterFirst {
		t.Errorf("second run made %d further embed calls, want none",
			h.embedder.embedCalls()-callsAfterFirst)
	}
	if res.Skipped != 5 || res.Embedded != 0 || res.Upserted != 0 || res.Batches != 0 {
		t.Errorf("second run = %+v, want 5 skipped and nothing embedded or written", res)
	}
	if got := h.count(t, "notes"); got != 5 {
		t.Errorf("namespace holds %d vectors after the re-run, want the same 5", got)
	}
}

func TestRunSkipsRepeatedContentWithinOneRequest(t *testing.T) {
	h := newHarness(t, 8)
	c := testCorpus("notes")
	same := newChunk("a.md", "identical body")
	other := newChunk("b.md", "identical body")
	res, err := h.pipeline.Run(context.Background(), Request{
		Corpus: c,
		Chunks: []retrieval.Chunk{same, other, same},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Embedded != 1 || res.Skipped != 2 {
		t.Errorf("result = %+v, want 1 embedded and 2 skipped for identical content", res)
	}
	if got := h.count(t, "notes"); got != 1 {
		t.Errorf("namespace holds %d vectors, want 1 for one distinct content", got)
	}
}

func TestRunEmptyChunkSliceTouchesNothing(t *testing.T) {
	h := newHarness(t, 4)
	res, err := h.pipeline.Run(context.Background(), Request{Corpus: testCorpus("notes")})
	if err != nil {
		t.Fatalf("empty run returned %v, want no error", err)
	}
	if res.Embedded != 0 || res.Upserted != 0 || res.Batches != 0 {
		t.Errorf("empty run = %+v, want an all-zero result", res)
	}
	if h.embedder.embedCalls() != 0 {
		t.Errorf("empty run made %d embed calls, want none", h.embedder.embedCalls())
	}
	names, err := h.vectors.Namespaces(context.Background())
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("empty run established namespaces %v, want none", names)
	}
}

func TestRunRefusesUnclassifiedCorpus(t *testing.T) {
	h := newHarness(t, 4)
	bad := testCorpus("notes")
	bad.Trust = ""
	_, err := h.pipeline.Run(context.Background(), Request{Corpus: bad, Chunks: chunks(1)})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput for an unclassified corpus", err)
	}
	if h.embedder.embedCalls() != 0 {
		t.Errorf("refused run still called the embedder %d times", h.embedder.embedCalls())
	}
}

func TestRunRefusesChunkWhoseIDIsNotItsContentHash(t *testing.T) {
	h := newHarness(t, 4)
	c := newChunk("a.md", "real body")
	c.ID = retrieval.ChunkID([]byte("some other body"))
	_, err := h.pipeline.Run(context.Background(), Request{
		Corpus: testCorpus("notes"),
		Chunks: []retrieval.Chunk{c},
	})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity for a chunk that is not content addressed", err)
	}
	if h.embedder.embedCalls() != 0 {
		t.Errorf("refused run still called the embedder %d times", h.embedder.embedCalls())
	}
}

func TestRunRefusesEmptyChunkFields(t *testing.T) {
	for name, c := range map[string]retrieval.Chunk{
		"no id":      {Path: "a.md", Content: []byte("body")},
		"no content": {ID: retrieval.ChunkID([]byte("body")), Path: "a.md"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 4)
			_, err := h.pipeline.Run(context.Background(), Request{
				Corpus: testCorpus("notes"),
				Chunks: []retrieval.Chunk{c},
			})
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("err = %v, want KindInvalidInput", err)
			}
		})
	}
}

func TestNewRefusesMissingCollaborators(t *testing.T) {
	store := storetest.NewMemStore()
	vectors := localvector.New(store)
	clock := runtime.NewFixedClock(fixedInstant)
	emb := &fakeEmbedder{model: testModel}
	cases := map[string]func() (*Pipeline, error){
		"no embedder": func() (*Pipeline, error) { return New(nil, vectors, store, clock, 1) },
		"no vectors":  func() (*Pipeline, error) { return New(emb, nil, store, clock, 1) },
		"no store":    func() (*Pipeline, error) { return New(emb, vectors, nil, clock, 1) },
		"no clock":    func() (*Pipeline, error) { return New(emb, vectors, store, nil, 1) },
		"negative size": func() (*Pipeline, error) {
			return New(emb, vectors, store, clock, -1)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := build()
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("err = %v, want KindInvalidInput", err)
			}
			if p != nil {
				t.Errorf("a refused New still returned a pipeline")
			}
		})
	}
}

func TestNewDefaultsBatchSize(t *testing.T) {
	h := newHarness(t, 0)
	if h.pipeline.batchSize != DefaultBatchSize {
		t.Errorf("batch size = %d, want DefaultBatchSize %d", h.pipeline.batchSize, DefaultBatchSize)
	}
}

func TestRunRefusesEmbedderWithUnsetModel(t *testing.T) {
	h := newHarnessWith(t, &fakeEmbedder{model: provider.EmbedModel{ID: "", Dimensions: 0}}, 4)
	_, err := h.pipeline.Run(context.Background(), Request{
		Corpus: testCorpus("notes"),
		Chunks: chunks(1),
	})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput for an unset model", err)
	}
}
