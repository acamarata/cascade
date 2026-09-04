// Purpose: the Epic F acceptance harness and its end-to-end story — real
// files on disk, chunked by the real chunkers, embedded through the real
// embed pipeline into the real flat vector driver, queried through the
// real scope filter and vector leg, fused by the real RRF fusion under
// the parameters the real config loader resolved, and cited by the real
// citation assembler.
//
// WHAT IS REAL AND WHAT IS NOT. Real: internal/retrieval's chunkers and
// ChunkID, internal/retrieval/corpus, internal/retrieval/embed,
// internal/retrieval/fusion (ScopeFilter and VectorLeg),
// internal/retrieval/rrf (Fuse, FuseWith, Rerank),
// internal/retrieval/citations, internal/runtime's config loader,
// providers/localvector and the reference key-value store. Doubles, and
// only these: the EMBEDDING PROVIDER (bagEmbedder below — no network in
// the unit lane, Art.7), and the LEXICAL LEG (lexicalLeg below), which
// stands in for the FTS5 leg because F/S-10.T2 has not landed. Both are
// named at their declarations. An acceptance test built on doubles proves
// only that the doubles agree, so nothing else here is one.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — files under t.TempDir(), injected clock, no
// network, no wall clock.
// SPORT: internal.retrieval.e2e/ADDED (P1-E06-W2-S12-T4).

package retrieval_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// vocab is the bagEmbedder's embedding space: one dimension per term.
var vocab = []string{
	"reciprocal", "rank", "fusion", "citation",
	"provenance", "storage", "driver", "quokka",
}

// e2eClock is the injected instant every run sees (Art.7).
var e2eClock = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// bagEmbedder is the ONE unavoidable double: an embedding provider. It is
// a deterministic bag-of-words projection onto vocab, so semantically
// close text lands close in the space without any network call and
// without a recorded fixture this ticket has no embedder to record. It is
// not a stand-in for a real model's quality — no assertion here depends
// on it ranking better than counting words does.
type bagEmbedder struct{}

func (bagEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "bag-of-words-v1", Dimensions: len(vocab)}
}

func (b bagEmbedder) Embed(
	_ context.Context, inputs []provider.EmbedInput,
) ([]provider.EmbedOutput, error) {
	out := make([]provider.EmbedOutput, len(inputs))
	for i, in := range inputs {
		out[i] = provider.EmbedOutput{Vector: bagVector(in.Text), Model: b.Model()}
	}
	return out, nil
}

// EmbedTexts adapts the provider contract to the narrower interface the
// query-time vector leg declares.
func (b bagEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = bagVector(t)
	}
	return out, nil
}

// legEmbedder adapts bagEmbedder to fusion.Embedder.
type legEmbedder struct{ bagEmbedder }

func (l legEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return l.EmbedTexts(ctx, texts)
}

// bagVector counts each vocab term in text, plus a constant floor so no
// document embeds to the zero vector (cosine is undefined there).
func bagVector(text string) []float32 {
	lower := strings.ToLower(text)
	out := make([]float32, len(vocab))
	for i, term := range vocab {
		out[i] = float32(strings.Count(lower, term)) + 0.01
	}
	return out
}

// storedChunk is one ingested chunk, kept so the lexical leg and the
// passage-text resolver can read the content back by id.
type storedChunk struct {
	chunk    retrieval.Chunk
	corpusID string
}

// e2eHarness is one fully wired retrieval stack over a real temp tree.
type e2eHarness struct {
	dir     string
	store   *corpus.Store
	vectors provider.VectorStore
	chunks  map[string]storedChunk
	leg     *fusion.VectorLeg
}

// newE2EHarness builds the stack. Every collaborator below the two named
// doubles is the shipping implementation.
func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()
	meta := storetest.NewMemStore()
	vectors := localvector.New(meta)
	return &e2eHarness{
		dir:     t.TempDir(),
		store:   corpus.NewStore(),
		vectors: vectors,
		chunks:  map[string]storedChunk{},
		leg:     fusion.NewVectorLeg(legEmbedder{}, vectors, nil),
	}
}

// ingest writes files to disk, reads them back, chunks them with the real
// chunkers, registers each chunk in the corpus model and embeds them.
func (h *e2eHarness) ingest(t *testing.T, c corpus.Corpus, files map[string]string) {
	t.Helper()
	if err := h.store.AddCorpus(c); err != nil {
		t.Fatalf("AddCorpus(%s): %v", c.ID, err)
	}
	meta := storetest.NewMemStore()
	pipeline, err := embed.New(bagEmbedder{}, h.vectors, meta,
		runtime.NewFixedClock(e2eClock), 0)
	if err != nil {
		t.Fatalf("embed.New: %v", err)
	}
	var all []retrieval.Chunk
	for _, name := range sortedKeys(files) {
		all = append(all, h.chunkFile(t, c, name, files[name])...)
	}
	if _, err := pipeline.Run(context.Background(),
		embed.Request{Corpus: c, Chunks: all}); err != nil {
		t.Fatalf("pipeline.Run(%s): %v", c.ID, err)
	}
}

// chunkFile writes one real file and returns its real chunks.
func (h *e2eHarness) chunkFile(
	t *testing.T, c corpus.Corpus, name, body string,
) []retrieval.Chunk {
	t.Helper()
	dir := filepath.Join(h.dir, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is under t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	chunker, err := retrieval.ChunkerFor(path)
	if err != nil {
		t.Fatalf("ChunkerFor(%s): %v", path, err)
	}
	chunks, err := chunker.Chunk(path, raw)
	if err != nil {
		t.Fatalf("Chunk(%s): %v", path, err)
	}
	for _, ch := range chunks {
		h.chunks[ch.ID] = storedChunk{chunk: ch, corpusID: c.ID}
		if err := h.store.AddRecord(corpus.Record{
			ID: ch.ID, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
		}); err != nil {
			t.Fatalf("AddRecord: %v", err)
		}
	}
	return chunks
}

// sortedKeys keeps ingest order a function of the file names, not of map
// iteration, so a run is reproducible.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lexicalLeg is the SECOND double, and the reason it exists is stated
// plainly: F/S-10.T2's FTS5 leg has not landed, and fusion needs two legs
// to fuse. It ranks by how many query terms a chunk's real content holds,
// ties broken on chunk id. It reads only chunks the scope filter resolves,
// which is how a real full-text leg narrows its scan.
func (h *e2eHarness) lexicalLeg(f *fusion.ScopeFilter, query string, topK int) rrf.RankedList {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		cand  rrf.Candidate
		score int
	}
	var hits []scored
	for _, id := range sortedChunkIDs(h.chunks) {
		record, ok := f.Resolve(id)
		if !ok {
			continue
		}
		body := strings.ToLower(string(h.chunks[id].chunk.Content))
		total := 0
		for _, term := range terms {
			total += strings.Count(body, term)
		}
		if total == 0 {
			continue
		}
		hits = append(hits, scored{score: total, cand: rrf.Candidate{
			ChunkID: id, Path: h.chunks[id].chunk.Path,
			CorpusID: record.CorpusID, Trust: record.Trust,
		}})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].cand.ChunkID < hits[j].cand.ChunkID
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := rrf.RankedList{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight}
	for _, hit := range hits {
		out.Hits = append(out.Hits, hit.cand)
	}
	return out
}

// sortedChunkIDs walks the ingested chunks in a fixed order.
func sortedChunkIDs(m map[string]storedChunk) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
