// Purpose: the query-time vector leg. It embeds the query text once, asks
// each scope-bound namespace for its nearest neighbours, and returns the
// merged ranked list the fusion core scores against the full-text leg.
//
// Inputs: a scope filter (already resolved, before this leg runs), the
// query text, and how many candidates to return.
//
// Outputs: one ranked list, plus whether the leg ran at all.
//
// Constraints: when no embedder is configured the leg is SKIPPED. It does
// not fail the query and it does not invent vectors: fusion degrades to
// the full-text leg alone and an event records that the vector half of
// retrieval was not available, so the degradation is visible to the doctor
// instead of silently looking like a thin index.
//
// SPORT: internal.retrieval.fusion.VectorLeg/ADDED (P1-E06-W2-S11-T1).

package fusion

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EventKindVectorLegUnavailable records that a query ran without its
// vector leg because no embedder was configured.
const EventKindVectorLegUnavailable events.EventKind = "retrieval.vector_leg.unavailable"

// eventNamespace and eventSource label this leg's events on the bus.
const (
	eventNamespace = "retrieval"
	eventSource    = "retrieval.fusion"
)

// MetadataKeyPath is the metadata key a stored vector carries its source
// path under. A driver preserves metadata verbatim, so this is the one
// place the key is spelled.
const MetadataKeyPath = "path"

// Embedder turns text into an embedding vector.
//
// The interface is declared here, at the consumer, deliberately narrowly:
// this leg needs exactly one operation, and declaring what it needs means
// any driver that can embed satisfies it without this package knowing
// which one is wired.
type Embedder interface {
	// Embed returns one vector per input text, in the same order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// EventSink is the subset of the event bus this leg publishes through.
type EventSink interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// VectorLeg is the query-time embedding-similarity leg.
type VectorLeg struct {
	embedder Embedder
	store    provider.VectorStore
	events   EventSink
}

// NewVectorLeg builds the leg. embedder, store and sink may each be nil:
// a nil embedder or store is the unconfigured case the leg is required to
// degrade through, and a nil sink is a caller that has no event bus to
// report the degradation to.
func NewVectorLeg(embedder Embedder, store provider.VectorStore, sink EventSink) *VectorLeg {
	return &VectorLeg{embedder: embedder, store: store, events: sink}
}

// Query returns the leg's ranked candidates for text.
//
// The second return value reports whether the leg ran. False with a nil
// error is the skip: no embedder or no vector store is configured, the
// unavailability event has been published, and the caller fuses the
// full-text leg on its own. It is never an error, because a local install
// with no embedding provider is a supported configuration and not a fault.
func (l *VectorLeg) Query(ctx context.Context, filter *ScopeFilter, text string, topK int) (rrf.RankedList, bool, error) {
	if filter == nil {
		return rrf.RankedList{}, false, cascade.New(cascade.KindInvalidInput, "fusion: nil scope filter")
	}
	if topK <= 0 {
		return rrf.RankedList{}, false, cascade.Newf(cascade.KindInvalidInput,
			"fusion: topK must be positive, got %d", topK)
	}
	if l.embedder == nil || l.store == nil {
		if err := l.reportUnavailable(ctx); err != nil {
			return rrf.RankedList{}, false, err
		}
		return rrf.RankedList{}, false, nil
	}
	if filter.Empty() {
		return rrf.RankedList{Strategy: rrf.StrategyVector, Weight: rrf.NeutralWeight}, true, nil
	}
	values, err := l.embed(ctx, text)
	if err != nil {
		return rrf.RankedList{}, false, err
	}
	hits, err := l.search(ctx, filter, values, topK)
	if err != nil {
		return rrf.RankedList{}, false, err
	}
	return rrf.RankedList{
		Strategy: rrf.StrategyVector,
		Weight:   rrf.NeutralWeight,
		Hits:     hits,
	}, true, nil
}

// embed turns the query text into one vector, refusing an embedder that
// answered with the wrong shape rather than proceeding on whatever it
// returned.
func (l *VectorLeg) embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := l.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "fusion: embedding the query")
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"fusion: embedder returned %d vectors for one query text", len(vectors))
	}
	return vectors[0], nil
}

// scoredHit is one driver match with the namespace it came from, held
// until every namespace has answered and the merged order can be decided.
type scoredHit struct {
	candidate rrf.Candidate
	score     float32
}

// search queries every scope-bound namespace and merges the answers.
//
// Each namespace is asked for topK, and the merged list is cut to topK
// afterwards. Asking for less per namespace would let one corpus's strong
// matches crowd out another's before the merge ever saw them.
func (l *VectorLeg) search(ctx context.Context, filter *ScopeFilter, values []float32, topK int) ([]rrf.Candidate, error) {
	var hits []scoredHit
	for _, ns := range filter.Namespaces() {
		matches, err := l.store.Query(ctx, ns, provider.VectorQuery{Values: values, TopK: topK})
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "fusion: querying namespace %q", ns)
		}
		hits = append(hits, resolveMatches(filter, matches)...)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].candidate.ChunkID < hits[j].candidate.ChunkID
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]rrf.Candidate, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.candidate)
	}
	return out, nil
}

// resolveMatches turns driver matches into candidates carrying the
// classification the scope filter already resolved.
//
// A match whose id the filter does not know is dropped here, on the raw
// driver response, before anything is ranked: a namespace holds a whole
// corpus while classification is per record, so the driver can legitimately
// return an id the corpus model withheld, and content with no resolved
// classification is withheld rather than surfaced.
func resolveMatches(filter *ScopeFilter, matches []provider.VectorMatch) []scoredHit {
	out := make([]scoredHit, 0, len(matches))
	for _, m := range matches {
		record, ok := filter.Resolve(m.ID)
		if !ok {
			continue
		}
		out = append(out, scoredHit{
			score: m.Score,
			candidate: rrf.Candidate{
				ChunkID:  record.ID,
				Path:     pathFromMetadata(m.Metadata),
				CorpusID: record.CorpusID,
				Trust:    record.Trust,
			},
		})
	}
	return out
}

// pathFromMetadata reads the stored source path, returning the empty
// string when the driver carried none rather than inventing one.
func pathFromMetadata(md map[string]any) string {
	if md == nil {
		return ""
	}
	if p, ok := md[MetadataKeyPath].(string); ok {
		return p
	}
	return ""
}

// reportUnavailable publishes the skip. A caller with no sink skips
// silently, which is the only case where the degradation is not recorded.
// A sink that fails to record it is a real fault in the event log and is
// returned as one; it is a different condition from the missing embedder,
// which is never an error.
func (l *VectorLeg) reportUnavailable(ctx context.Context) error {
	if l.events == nil {
		return nil
	}
	_, err := l.events.Publish(ctx, eventNamespace, EventKindVectorLegUnavailable, eventSource, nil)
	return err
}
