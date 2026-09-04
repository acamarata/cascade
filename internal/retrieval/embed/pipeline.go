// Package embed is the write half of retrieval: it turns the chunks the
// ingest stage produced into vectors and lands them in the vector index
// the query-time fusion path reads.
//
// Purpose: group chunks into batches, embed each batch through the
// provider.Embedder seam, and upsert the results into the per-corpus
// vector namespace, skipping content that is already embedded.
//
// Inputs: a corpus (fully classified) and its chunks, plus the three
// injected collaborators: an Embedder, a VectorStore, and a Store for the
// pipeline's own bookkeeping.
//
// Outputs: a Result counting what was skipped, embedded and written, or a
// pkg/cascade taxonomy error. The Result is returned even when the error
// is non-nil, because a caller of a partially applied run needs to know
// how far it got.
//
// Constraints: local only, no network (any network call belongs to an
// Embedder implementation, never here); no wall-clock reads (the ledger
// timestamps through an injected runtime.Clock); no map iteration reaches
// storage, so two runs over the same input issue the same writes in the
// same order.
//
// SPORT: internal.retrieval.embed.Pipeline/ADDED (P1-E06-W2-S10-T3).
package embed

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DefaultBatchSize is the batch size New uses when a caller passes zero.
// Batching exists because a per-item embed call dominates wall time on
// every real backend (pkg/provider's Embedder contract says so); the exact
// number is a throughput tuning knob, not a correctness one.
const DefaultBatchSize = 32

// Pipeline embeds chunks and upserts the vectors.
//
// The zero value is not usable; construct with New.
type Pipeline struct {
	embedder  provider.Embedder
	vectors   provider.VectorStore
	ledger    ledger
	namespace namespaceGuard
	clock     runtime.Clock
	batchSize int
}

// New builds a Pipeline.
//
// meta is the key-value store the pipeline keeps its own two pieces of
// bookkeeping in: the dedupe ledger (dedupe.go) and the namespace-to-model
// binding (upsert.go). It is deliberately a separate argument from the
// vector store: provider.VectorStore has no place to record which model a
// namespace's vectors came from, and inventing one inside the vector
// records would put the guard inside the data it is guarding.
//
// batchSize of zero means DefaultBatchSize. Every argument is required; a
// nil collaborator is refused here rather than becoming a nil dereference
// on the first run.
func New(
	embedder provider.Embedder,
	vectors provider.VectorStore,
	meta provider.Store,
	clock runtime.Clock,
	batchSize int,
) (*Pipeline, error) {
	switch {
	case embedder == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "embed: no embedder")
	case vectors == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "embed: no vector store")
	case meta == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "embed: no metadata store")
	case clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "embed: no clock")
	case batchSize < 0:
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"embed: batch size %d is negative", batchSize)
	}
	if batchSize == 0 {
		batchSize = DefaultBatchSize
	}
	return &Pipeline{
		embedder:  embedder,
		vectors:   vectors,
		ledger:    ledger{store: meta},
		namespace: namespaceGuard{store: meta},
		clock:     clock,
		batchSize: batchSize,
	}, nil
}

// Request is one pipeline run: the chunks of one corpus.
//
// Chunks carry no classification of their own. The corpus supplies it, and
// the corpus is what decides the vector namespace, so a chunk cannot be
// written anywhere its corpus does not reach.
type Request struct {
	// Corpus owns the chunks. It must validate: an unclassified corpus is
	// refused rather than defaulted, because the classification is the
	// only thing bounding where this content can later be read from.
	Corpus corpus.Corpus
	// Chunks are the ingest stage's output, in source order.
	Chunks []retrieval.Chunk
}

// Result counts one run's work.
type Result struct {
	// Namespace is the vector namespace the run wrote to.
	Namespace string
	// Skipped is how many chunks were already embedded and so were
	// neither sent to the embedder nor written again.
	Skipped int
	// Embedded is how many chunks were sent to the embedder.
	Embedded int
	// Upserted is how many vectors were written. It equals Embedded on a
	// complete run and is lower when a batch failed part-way through.
	Upserted int
	// Batches is how many embed calls were issued.
	Batches int
}

// Run embeds req's chunks and upserts them.
//
// Partial failure, stated exactly because a half-indexed corpus that looks
// whole is worse than a failed run: batches commit one at a time. A batch
// that fails, at the embedder or at the vector store, writes no vectors
// and records no ledger entries of its own, so no partial batch lands.
// Batches committed before it stay committed, and the ledger names exactly
// those, so a re-run resumes at the failed batch and re-embeds nothing
// that already succeeded. The returned Result reports how far the run got
// alongside the error.
//
// An empty chunk slice is a no-op: no embedder call, no store call, a
// zero Result and a nil error. Ingesting a corpus that produced no chunks
// is an ordinary outcome, and pkg/provider's Embedder contract makes the
// same choice for an empty batch.
func (p *Pipeline) Run(ctx context.Context, req Request) (Result, error) {
	if err := req.Corpus.Validate(); err != nil {
		return Result{}, err
	}
	ns := fusion.NamespaceFor(req.Corpus.ID)
	result := Result{Namespace: ns}
	if len(req.Chunks) == 0 {
		return result, nil
	}
	if err := validateChunks(req.Chunks); err != nil {
		return result, err
	}
	model := p.embedder.Model()
	if model.ID == "" || model.Dimensions <= 0 {
		return result, cascade.Newf(cascade.KindInvalidInput,
			"embed: embedder reports an unset model (id %q, %d dimensions)",
			model.ID, model.Dimensions)
	}
	if err := p.namespace.claim(ctx, ns, model); err != nil {
		return result, err
	}
	pending, skipped, err := p.plan(ctx, ns, req.Chunks, model)
	if err != nil {
		return result, err
	}
	result.Skipped = skipped
	return p.process(ctx, ns, pending, model, result)
}

// validateChunks refuses the whole request if any chunk is malformed,
// before any embedding is paid for.
//
// The ID check is not redundant with the ingest stage: dedupe and the
// vector key both rest on the ID being the hash of the content, so this
// package verifies that rather than trusting it. A caller that computed
// the ID over something other than the content it handed us would
// otherwise silently poison the dedupe ledger.
func validateChunks(chunks []retrieval.Chunk) error {
	for i, c := range chunks {
		if c.ID == "" {
			return cascade.Newf(cascade.KindInvalidInput, "embed: chunk %d has no id", i)
		}
		if len(c.Content) == 0 {
			return cascade.Newf(cascade.KindInvalidInput, "embed: chunk %s has no content", c.ID)
		}
		if got := retrieval.ChunkID(c.Content); got != c.ID {
			return cascade.Newf(cascade.KindIntegrity,
				"embed: chunk %s is not content-addressed (content hashes to %s)", c.ID, got)
		}
	}
	return nil
}

// plan returns the chunks that still need embedding, in request order,
// and how many were skipped.
//
// Two things are deduped here. Repeats within the request collapse to
// their first occurrence, so a corpus containing the same content twice
// costs one embed call. Content the ledger already records for this
// namespace and model is dropped entirely: no embed call, no write.
func (p *Pipeline) plan(
	ctx context.Context, ns string, chunks []retrieval.Chunk, model provider.EmbedModel,
) (pending []retrieval.Chunk, skipped int, err error) {
	seen := make(map[string]bool, len(chunks))
	for _, c := range chunks {
		if seen[c.ID] {
			skipped++
			continue
		}
		seen[c.ID] = true
		done, lerr := p.ledger.seen(ctx, ns, c.ID, model)
		if lerr != nil {
			return nil, 0, lerr
		}
		if done {
			skipped++
			continue
		}
		pending = append(pending, c)
	}
	return pending, skipped, nil
}

// process embeds and commits the pending chunks batch by batch.
func (p *Pipeline) process(
	ctx context.Context, ns string, pending []retrieval.Chunk,
	model provider.EmbedModel, result Result,
) (Result, error) {
	for start := 0; start < len(pending); start += p.batchSize {
		end := start + p.batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		result.Batches++
		outputs, err := p.embed(ctx, batch, model)
		if err != nil {
			return result, err
		}
		result.Embedded += len(batch)
		if err := p.commit(ctx, ns, batch, outputs, model); err != nil {
			return result, err
		}
		result.Upserted += len(batch)
	}
	return result, nil
}

// embed issues one grouped Embed call and refuses a response that does not
// satisfy the Embedder contract's structural half.
func (p *Pipeline) embed(
	ctx context.Context, batch []retrieval.Chunk, model provider.EmbedModel,
) ([]provider.EmbedOutput, error) {
	inputs := make([]provider.EmbedInput, len(batch))
	for i, c := range batch {
		inputs[i] = provider.EmbedInput{Text: string(c.Content)}
	}
	outputs, err := p.embedder.Embed(ctx, inputs)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"embed: embedding a batch of %d chunks", len(batch))
	}
	if !model.ValidBatch(inputs, outputs) {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"embed: embedder returned %d outputs for %d inputs, or outputs outside model %s/%d",
			len(outputs), len(inputs), model.ID, model.Dimensions)
	}
	return outputs, nil
}

// commit writes one batch's vectors and then records them in the ledger.
//
// The order is deliberate and asymmetric. A crash between the two leaves
// vectors written but unrecorded, so the next run re-embeds them and
// upserts the same ids again, which is redundant work over an
// insert-or-replace write. The other order would leave a ledger entry with
// no vector behind it, and every later run would skip content the index
// does not hold.
func (p *Pipeline) commit(
	ctx context.Context, ns string, batch []retrieval.Chunk,
	outputs []provider.EmbedOutput, model provider.EmbedModel,
) error {
	vectors, err := buildVectors(batch, outputs, model)
	if err != nil {
		return err
	}
	if err := p.vectors.Upsert(ctx, ns, vectors); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"embed: upserting %d vectors into %q", len(vectors), ns)
	}
	return p.ledger.record(ctx, ns, batch, model, p.clock.Now())
}
