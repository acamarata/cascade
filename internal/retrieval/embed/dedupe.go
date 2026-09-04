// Purpose: the content-addressed dedupe ledger. It records which chunk
// content has already been embedded into which vector namespace, under
// which model, so a re-run over an unchanged corpus performs no embed
// calls and writes no duplicate vectors.
//
// Inputs: a vector namespace, chunk ids (which are content hashes), and
// the embedding model those chunks were embedded under.
//
// Outputs: whether content is already embedded, or a pkg/cascade taxonomy
// error.
//
// Constraints: the ledger keys on the chunk id and nothing else, and the
// chunk id is the BLAKE3 digest of the chunk's canonicalized content
// (internal/retrieval's ChunkID). That is what makes dedupe content
// addressed rather than path or timestamp addressed: edit a file and the
// changed chunks get new ids and are embedded, while the untouched ones
// keep theirs and are skipped. Timestamps come from the injected clock,
// never the wall clock.
//
// SPORT: internal.retrieval.embed.ledger/ADDED (P1-E06-W2-S10-T3).

package embed

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ledgerNamespace is the Store namespace the dedupe records live in. It is
// disjoint from modelNamespace (upsert.go) and from any vector namespace,
// so a corpus id can never collide with this package's bookkeeping.
const ledgerNamespace = "retrieval/embed/ledger"

// ledgerEntry is one "this content is embedded" record.
//
// The model identity is stored with every entry, not only in the
// namespace binding, so a stray entry read on its own still says which
// embedding space it is claiming coverage of.
type ledgerEntry struct {
	// ModelID names the model the content was embedded under.
	ModelID string `json:"model_id"`
	// Dimensions is that model's vector width.
	Dimensions int `json:"dimensions"`
	// EmbeddedAt is when the entry was recorded, from the injected clock.
	// It is provenance for the index-lifecycle path, never an input to a
	// dedupe decision: dedupe is by content, so an age never changes the
	// answer.
	EmbeddedAt time.Time `json:"embedded_at"`
}

// ledger is the dedupe record set, persisted through a provider.Store.
type ledger struct {
	store provider.Store
}

// ledgerKey is the ledger's key for one chunk in one namespace.
//
// Namespace and chunk id are both in the key rather than the namespace
// alone, because the same content can legitimately be embedded into two
// corpora, and each corpus's index needs its own copy of the vector.
func ledgerKey(namespace, chunkID string) string {
	return namespace + "/" + chunkID
}

// seen reports whether chunkID's content is already embedded in namespace
// under model.
//
// A recorded entry under a DIFFERENT model is refused rather than treated
// as a miss. Reporting a miss would embed the content again and upsert it
// over the existing vector, quietly mixing two embedding spaces in one
// namespace. The namespace binding in upsert.go should already have
// refused the run before this point; this is the second, per-record
// check, and it fails closed for the same reason the first one does.
func (l ledger) seen(
	ctx context.Context, namespace, chunkID string, model provider.EmbedModel,
) (bool, error) {
	data, err := l.store.Get(ctx, ledgerNamespace, ledgerKey(namespace, chunkID))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindUnavailable, err,
			"embed: reading the dedupe ledger for %s", chunkID)
	}
	var entry ledgerEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return false, cascade.Wrapf(cascade.KindIntegrity, err,
			"embed: decoding the dedupe ledger entry for %s", chunkID)
	}
	if entry.ModelID != model.ID || entry.Dimensions != model.Dimensions {
		return false, cascade.Newf(cascade.KindConflict,
			"embed: %s is embedded in %q under model %s/%d, not %s/%d",
			chunkID, namespace, entry.ModelID, entry.Dimensions,
			model.ID, model.Dimensions)
	}
	return true, nil
}

// record writes one ledger entry per chunk, in the order given.
//
// Chunks are iterated as a slice, never as a map, so two runs over the
// same batch issue the same writes in the same order.
func (l ledger) record(
	ctx context.Context, namespace string, chunks []retrieval.Chunk,
	model provider.EmbedModel, now time.Time,
) error {
	entry := ledgerEntry{
		ModelID:    model.ID,
		Dimensions: model.Dimensions,
		EmbeddedAt: now.UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "embed: encoding a dedupe ledger entry")
	}
	for _, c := range chunks {
		if err := l.store.Put(ctx, ledgerNamespace, ledgerKey(namespace, c.ID), data); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err,
				"embed: recording the dedupe ledger entry for %s", c.ID)
		}
	}
	return nil
}
