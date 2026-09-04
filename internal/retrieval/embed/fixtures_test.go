// Purpose: the fixtures every test in this package shares: the Embedder
// double, the corpus and chunk builders, and the harness that wires the
// pipeline over the real flat vector driver and the reference key-value
// store.
//
// Constraints: the Embedder double lives here, under _test.go, and
// nowhere else (Art.1.1). Everything below it in the stack is production
// code. No fixture reads the wall clock.

package embed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// testModel is the embedding space every fixture embedder produces in
// unless a test deliberately switches to another one.
var testModel = provider.EmbedModel{ID: "test-embed-v1", Dimensions: 4}

// fixedInstant is the ledger timestamp every test sees. Art.7: no test
// reads the wall clock.
var fixedInstant = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// fakeEmbedder is the Embedder test double. It is deterministic: the
// vector for a text is derived from the text itself, so the same content
// always embeds to the same values and a test can assert on what landed.
//
// failOnCall makes the Nth Embed call fail, which is how the partial
// failure tests place a fault in the middle of a multi-batch run.
type fakeEmbedder struct {
	model      provider.EmbedModel
	calls      [][]string
	failOnCall int
	// wrongModel makes every output claim this model instead of the
	// embedder's own, simulating a provider that switched mid-batch.
	wrongModel *provider.EmbedModel
	// shortVector drops one value from every returned vector.
	shortVector bool
	// dropOutput returns one fewer output than inputs.
	dropOutput bool
}

func (f *fakeEmbedder) Model() provider.EmbedModel { return f.model }

func (f *fakeEmbedder) Embed(
	_ context.Context, inputs []provider.EmbedInput,
) ([]provider.EmbedOutput, error) {
	texts := make([]string, len(inputs))
	for i, in := range inputs {
		texts[i] = in.Text
	}
	f.calls = append(f.calls, texts)
	if f.failOnCall != 0 && len(f.calls) == f.failOnCall {
		return nil, errors.New("embedding backend refused the batch")
	}
	outputs := make([]provider.EmbedOutput, 0, len(inputs))
	for _, in := range inputs {
		out := provider.EmbedOutput{Vector: vectorFor(in.Text, f.model.Dimensions), Model: f.model}
		if f.wrongModel != nil {
			out.Model = *f.wrongModel
		}
		if f.shortVector && len(out.Vector) > 0 {
			out.Vector = out.Vector[:len(out.Vector)-1]
		}
		outputs = append(outputs, out)
	}
	if f.dropOutput && len(outputs) > 0 {
		outputs = outputs[:len(outputs)-1]
	}
	return outputs, nil
}

// embedCalls reports how many grouped Embed calls the double received.
func (f *fakeEmbedder) embedCalls() int { return len(f.calls) }

// vectorFor derives a stable vector from text so equal content embeds
// equally and different content does not.
func vectorFor(text string, dims int) []float32 {
	values := make([]float32, dims)
	for i := range values {
		var sum float32
		for j, b := range []byte(text) {
			if j%dims == i {
				sum += float32(b)
			}
		}
		values[i] = sum + 1
	}
	return values
}

// testCorpus is a fully classified corpus. Nothing here defaults: the
// model refuses a corpus with an unset axis, which is the behaviour the
// invalid-corpus test relies on.
func testCorpus(id string) corpus.Corpus {
	return corpus.Corpus{
		ID:         id,
		ScopeRef:   corpus.ScopeRef("scope-" + id),
		Privacy:    corpus.PrivacyProject,
		Visibility: corpus.VisibilityScopeLocal,
		Trust:      corpus.TrustTrusted,
	}
}

// newChunk builds a chunk whose id is the real content hash, exactly as
// the ingest stage produces it.
func newChunk(path, content string) retrieval.Chunk {
	return retrieval.Chunk{
		ID:      retrieval.ChunkID([]byte(content)),
		Path:    path,
		Content: []byte(content),
		EndByte: len(content),
		Lang:    "markdown",
	}
}

// harness holds one pipeline and the real collaborators behind it.
type harness struct {
	pipeline *Pipeline
	embedder *fakeEmbedder
	store    *storetest.MemStore
	vectors  provider.VectorStore
}

// newHarness wires the pipeline over the real flat vector driver and the
// reference key-value store.
func newHarness(t *testing.T, batchSize int) *harness {
	t.Helper()
	return newHarnessWith(t, &fakeEmbedder{model: testModel}, batchSize)
}

func newHarnessWith(t *testing.T, emb *fakeEmbedder, batchSize int) *harness {
	t.Helper()
	store := storetest.NewMemStore()
	vectors := localvector.New(store)
	p, err := New(emb, vectors, store, runtime.NewFixedClock(fixedInstant), batchSize)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{pipeline: p, embedder: emb, store: store, vectors: vectors}
}

// count returns how many vectors the corpus's namespace holds.
func (h *harness) count(t *testing.T, corpusID string) int {
	t.Helper()
	n, err := h.vectors.Count(context.Background(), fusion.NamespaceFor(corpusID))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}

func chunks(n int) []retrieval.Chunk {
	out := make([]retrieval.Chunk, n)
	for i := range out {
		out[i] = newChunk("doc.md", "chunk body number "+string(rune('a'+i)))
	}
	return out
}
