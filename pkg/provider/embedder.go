// Purpose: declare the Embedder family contract: the dep-free, batchable
//   text-to-vector seam every embedding provider (api-backed, local sidecar)
//   implements, plus the model identity that keeps two embedding spaces from
//   being silently mixed.
// Inputs: a context and a batch of EmbedInput texts.
// Outputs: one EmbedOutput per input, positionally corresponding, or a
//   pkg/cascade taxonomy error.
// Constraints: pkg/provider imports only stdlib (Art.10.2): no internal/,
//   no third-party, no CGO; this file declares contracts only, every
//   implementation ships elsewhere (Art.1).
// SPORT: pkg.provider.Embedder/ADDED (P1-E06-W2-S10-T5).

package provider

import "context"

// Embedder turns text into dense vectors. It is the seam between the
// retrieval pipeline and whatever actually computes embeddings: a hosted
// API, a local sidecar, or an in-process model.
//
// Embedder is batchable by design. The pipeline groups chunks and issues one
// Embed call per group, because per-item calls dominate wall time on every
// real backend. A one-element batch is an ordinary call, not a special case:
// implementations must handle len(inputs) == 1 by the same path as any other
// size, and callers must never assume a minimum batch size.
//
// SDK-INTENT: this is a contract only. No implementation and no caller ship
// with it yet: the api-backed embedder and the embed pipeline that groups
// chunks for it land in later tickets, and third-party providers implement
// it from outside this module.
type Embedder interface {
	// Model returns the identity of the embedding space this Embedder
	// produces vectors in. It is expected to be constant for the lifetime
	// of the value and cheap to call (no network, no error return): a
	// caller may invoke it on every batch to tag the vectors it stores.
	//
	// Model exists so that vectors from different models cannot be mixed
	// silently. Cosine similarity between two unrelated embedding spaces
	// returns a plausible number rather than an error, so the mismatch is
	// undetectable after the fact. Callers that persist vectors MUST
	// record the EmbedModel alongside them and compare with
	// EmbedModel.Equal before querying an existing index.
	Model() EmbedModel

	// Embed computes one vector per input.
	//
	// Contract, binding on every implementation:
	//
	//   - Positional correspondence: the returned slice has exactly
	//     len(inputs) elements, and outputs[i] is the embedding of
	//     inputs[i]. Reordering, deduplicating, dropping, or padding the
	//     batch is a contract violation even when the same set of vectors
	//     comes back.
	//   - All-or-nothing: Embed either returns a complete batch and a nil
	//     error, or a nil slice and a non-nil error. There is no partial
	//     success and no per-item error channel; a caller that receives a
	//     nil error may index every position without checking for a zero
	//     value. An implementation whose backend fails on one item fails
	//     the whole call.
	//   - Empty input: Embed(ctx, nil) and Embed(ctx, []EmbedInput{})
	//     return an empty slice and a nil error. An empty batch is not an
	//     error, so a caller need not special-case an empty chunk group.
	//   - Model agreement: every returned EmbedOutput carries Model equal
	//     to this Embedder's Model(), and a Vector of exactly
	//     Model().Dimensions elements. EmbedModel.ValidBatch checks both.
	//   - Cancellation: Embed honors ctx. A canceled or expired context
	//     aborts the call and returns a cascade.KindCanceled or
	//     cascade.KindTimeout error rather than a partial batch.
	//
	// Errors returned at this boundary are pkg/cascade taxonomy errors:
	// KindInvalidInput for a malformed batch (an over-long text, an input
	// the backend rejects), KindUnavailable or KindTimeout for a backend
	// that could not be reached, KindQuotaExhausted when a provider quota
	// is spent, KindCanceled for a canceled context.
	Embed(ctx context.Context, inputs []EmbedInput) ([]EmbedOutput, error)
}

// EmbedInput is one item of an embedding batch.
type EmbedInput struct {
	// Text is the content to embed. An implementation embeds it as given:
	// truncation, normalization, and prefixing are the caller's decisions,
	// because they change the resulting vector and so must be made once,
	// at the pipeline, rather than differently by each provider.
	Text string
}

// EmbedOutput is one embedding, positionally corresponding to the
// EmbedInput at the same index of the Embed call that produced it.
type EmbedOutput struct {
	// Vector is the embedding itself, with exactly Model.Dimensions
	// elements.
	Vector []float32
	// Model identifies the embedding space Vector belongs to. It equals
	// the producing Embedder's Model(), and travels with the vector so a
	// consumer that receives an EmbedOutput out of context can still tell
	// which index it may be written to.
	Model EmbedModel
}

// EmbedModel identifies an embedding space: which model produced a vector
// and how wide that vector is. Two vectors are comparable only when the
// EmbedModel values that produced them are Equal.
type EmbedModel struct {
	// ID names the model, uniquely within a provider's namespace, and
	// includes whatever the provider varies between incompatible spaces
	// (a version or revision suffix, for instance). Two spaces that are
	// not interchangeable MUST NOT share an ID.
	ID string
	// Dimensions is the length of every vector this model produces. A
	// zero value means the model identity is unset, which ValidBatch
	// treats as invalid rather than as "any width".
	Dimensions int
}

// Equal reports whether m and other denote the same embedding space, so
// vectors produced under them may be compared. Both fields must match: an
// ID collision across differing widths is a configuration error, not a
// compatible space.
func (m EmbedModel) Equal(other EmbedModel) bool {
	return m.ID == other.ID && m.Dimensions == other.Dimensions
}

// ValidBatch reports whether outputs is a well-formed Embed response from an
// Embedder whose Model is m, for the given inputs. It is the structural half
// of the Embed contract, checkable by a caller that does not trust a
// third-party implementation:
//
//   - one output per input, so a dropped or padded batch is caught;
//   - every output's Model equal to m, so a provider that silently switched
//     models mid-batch is caught;
//   - every vector exactly m.Dimensions wide, so a truncated or
//     wrong-model vector is caught before it reaches an index.
//
// ValidBatch cannot check positional correspondence: a reordered batch is
// structurally identical to a correct one, which is why ordering is stated
// as a binding contract on Embed rather than left to a runtime check. A
// caller that must detect reordering has to embed a known probe.
//
// An unset m (empty ID or non-positive Dimensions) is never valid.
func (m EmbedModel) ValidBatch(inputs []EmbedInput, outputs []EmbedOutput) bool {
	if m.ID == "" || m.Dimensions <= 0 {
		return false
	}
	if len(outputs) != len(inputs) {
		return false
	}
	for _, out := range outputs {
		if !out.Model.Equal(m) || len(out.Vector) != m.Dimensions {
			return false
		}
	}
	return true
}
