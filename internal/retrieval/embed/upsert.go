// Purpose: the vector write path and the guard that stands in front of
// it. A vector namespace holds exactly one embedding space; this file
// binds a namespace to the first model written into it and refuses every
// later write from a different model or a different vector width.
//
// Inputs: a vector namespace, the model the pipeline is embedding under,
// and one batch's chunks with their embeddings.
//
// Outputs: the provider.Vector values to upsert, or a pkg/cascade taxonomy
// error.
//
// Constraints: this guard is the reason the pipeline needs a key-value
// store of its own. provider.VectorStore establishes a dimensionality per
// namespace but knows nothing about model identity, and two different
// models of the same width would pass its check while producing vectors
// that are not comparable at all. Cosine similarity across two embedding
// spaces returns a plausible number rather than an error, so the mistake
// is undetectable after the fact and has to be refused at write time.
//
// SPORT: internal.retrieval.embed.namespaceGuard/ADDED (P1-E06-W2-S10-T3).

package embed

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// modelNamespace is the Store namespace holding one binding per vector
// namespace: which embedding model that namespace's vectors belong to.
const modelNamespace = "retrieval/embed/model"

// MetadataKeyModel is the metadata key each stored vector carries its
// model id under. A driver preserves metadata verbatim, so a vector read
// back out of context still names the embedding space it belongs to.
const MetadataKeyModel = "embed_model"

// namespaceGuard binds vector namespaces to embedding models.
type namespaceGuard struct {
	store provider.Store
}

// binding is the persisted namespace-to-model record.
type binding struct {
	ModelID    string `json:"model_id"`
	Dimensions int    `json:"dimensions"`
}

// claim binds ns to model, or confirms it is already bound to it.
//
// The first writer into a namespace wins the binding and every later
// writer is checked against it. A disagreement is a KindConflict refusal
// that writes nothing: the namespace keeps the model it has, and the
// caller has to choose a different namespace or re-index the existing one
// under the new model.
func (g namespaceGuard) claim(ctx context.Context, ns string, model provider.EmbedModel) error {
	current, found, err := g.read(ctx, ns)
	if err != nil {
		return err
	}
	if found {
		return checkBinding(ns, current, model)
	}
	if err := g.bind(ctx, ns, model); err != nil {
		if !cascade.HasKind(err, cascade.KindConflict) {
			return err
		}
		// Another writer bound the namespace between the read and the
		// conditional write. Re-read and check against what it bound,
		// rather than assuming the loser's model is the right one.
		current, found, rerr := g.read(ctx, ns)
		if rerr != nil {
			return rerr
		}
		if !found {
			return err
		}
		return checkBinding(ns, current, model)
	}
	return nil
}

// read returns ns's binding, or found=false if it has none yet.
func (g namespaceGuard) read(ctx context.Context, ns string) (binding, bool, error) {
	data, err := g.store.Get(ctx, modelNamespace, ns)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return binding{}, false, nil
		}
		return binding{}, false, cascade.Wrapf(cascade.KindUnavailable, err,
			"embed: reading the model binding for %q", ns)
	}
	var b binding
	if err := json.Unmarshal(data, &b); err != nil {
		return binding{}, false, cascade.Wrapf(cascade.KindIntegrity, err,
			"embed: decoding the model binding for %q", ns)
	}
	return b, true, nil
}

// bind writes the binding conditionally, so two pipelines starting on an
// empty namespace at the same time cannot both believe they set it.
func (g namespaceGuard) bind(ctx context.Context, ns string, model provider.EmbedModel) error {
	data, err := json.Marshal(binding{ModelID: model.ID, Dimensions: model.Dimensions})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "embed: encoding a model binding")
	}
	err = g.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, modelNamespace, ns, nil, data)
	})
	if err != nil {
		if cascade.HasKind(err, cascade.KindConflict) {
			return err
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"embed: binding %q to model %s", ns, model.ID)
	}
	return nil
}

// checkBinding refuses a model that disagrees with what ns already holds.
func checkBinding(ns string, current binding, model provider.EmbedModel) error {
	if current.ModelID == model.ID && current.Dimensions == model.Dimensions {
		return nil
	}
	return cascade.Newf(cascade.KindConflict,
		"embed: namespace %q holds model %s/%d; refusing vectors from %s/%d "+
			"because embeddings from two models are not comparable",
		ns, current.ModelID, current.Dimensions, model.ID, model.Dimensions)
}

// buildVectors pairs each chunk with its embedding, positionally, per the
// Embedder contract.
//
// The vector id is the chunk id, which is the content hash, so the upsert
// is an insert-or-replace on content: the same content written twice
// occupies one row. Metadata carries the source path (the key the query
// leg reads its citations from) and the model id.
//
// Each output is re-checked against the model here, one vector at a time.
// The batch-level check already ran, and this one catches the narrower
// case of a well-formed batch whose individual member is about to be
// written into a namespace it does not belong in.
func buildVectors(
	chunks []retrieval.Chunk, outputs []provider.EmbedOutput, model provider.EmbedModel,
) ([]provider.Vector, error) {
	if len(chunks) != len(outputs) {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"embed: %d embeddings for %d chunks", len(outputs), len(chunks))
	}
	vectors := make([]provider.Vector, len(chunks))
	for i, c := range chunks {
		out := outputs[i]
		if !out.Model.Equal(model) || len(out.Vector) != model.Dimensions {
			return nil, cascade.Newf(cascade.KindIntegrity,
				"embed: chunk %s came back as model %s/%d with %d values, not %s/%d",
				c.ID, out.Model.ID, out.Model.Dimensions, len(out.Vector),
				model.ID, model.Dimensions)
		}
		vectors[i] = provider.Vector{
			ID:     c.ID,
			Values: out.Vector,
			Metadata: map[string]any{
				fusion.MetadataKeyPath: c.Path,
				MetadataKeyModel:       model.ID,
			},
		}
	}
	return vectors, nil
}
