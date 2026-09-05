package retrieval

// Purpose: the full-text index itself — the write path that turns chunks
//   into document rows and postings, the delete path that retracts them,
//   and the ranked query path over them. It is the keyword-retrieval leg
//   of the reciprocal-rank fusion, registered under the leg name
//   rrf.StrategyFTS (which is why this file and its schema carry that name
//   rather than describing the storage layout; see fts5_schema.go's
//   recorded contract deviation for what the layout actually is).
// Inputs: chunks from the ingest pipeline (chunk.go), and a search request
//   carrying the query text and the scope narrowing the corpus model
//   already resolved.
// Outputs: ranked Hits, or a pkg/cascade taxonomy error.
// Constraints: SCOPE IS NOT DECIDED HERE. The authorized record set
//   arrives already resolved (fusion.ScopePredicate) and this file only
//   ever narrows it further: a chunk id absent from the predicate can
//   never appear in a Hit, and an empty predicate returns nothing rather
//   than everything. No clock, no randomness, no network; ordering is by
//   score then chunk id, so an identical corpus and query rank identically
//   on any machine.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Hit is one ranked full-text match.
type Hit struct {
	// ChunkID is the content-addressed chunk id the whole epic joins on.
	ChunkID string
	// Path is the source file the chunk was carved from.
	Path string
	// CorpusID names the corpus the chunk was indexed under.
	CorpusID string
	// Snippet is a bounded window of the indexed text around the first
	// matched term, cut from the bytes that were actually indexed.
	Snippet string
	// Score is the BM25 relevance score. Higher ranks first; ties are
	// broken by ascending ChunkID, which is the whole tie-break rule.
	Score float64
}

// SearchRequest is one full-text query: the caller's raw text plus the
// scope narrowing that was resolved before this leg ran.
//
// Scope is a value, not a store handle, deliberately: this package can
// only ever be handed the already-narrowed set, so there is no widening
// it could perform even by mistake.
type SearchRequest struct {
	// Text is the caller's raw, untrusted query string.
	Text string
	// Scope is the authorized candidate set, from
	// fusion.ScopeFilter.Predicate.
	Scope fusion.ScopePredicate
}

// Indexer is the full-text index's write side.
type Indexer interface {
	// Write indexes chunks under corpusID. It is idempotent on chunk id:
	// writing the same chunk twice leaves exactly one document row and
	// one posting per token.
	Write(ctx context.Context, corpusID string, chunks []Chunk) error
	// Delete removes chunkIDs and every posting they wrote. Deleting an
	// absent chunk is not an error.
	Delete(ctx context.Context, chunkIDs []string) error
}

// Querier is the full-text index's read side.
type Querier interface {
	// Search returns at most topK hits, best first.
	Search(ctx context.Context, req SearchRequest, topK int) ([]Hit, error)
}

// Index is the store-backed full-text index.
type Index struct {
	store provider.Store
}

// Compile-time proof the concrete index satisfies both halves of the
// contract this package publishes.
var (
	_ Indexer = (*Index)(nil)
	_ Querier = (*Index)(nil)
)

// NewIndex builds an index over store, which is required: an index with no
// store would answer every query with nothing, which is indistinguishable
// from an index that holds nothing.
func NewIndex(store provider.Store) (*Index, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "retrieval: no store")
	}
	return &Index{store: store}, nil
}

// Write indexes chunks under corpusID in one transaction.
//
// The whole batch is one transaction so a failure part-way leaves the
// postings and the corpus statistics agreeing with the document rows. A
// half-written batch would leave postings pointing at rows that do not
// exist, which is the stale-posting condition the delete path exists to
// prevent.
func (x *Index) Write(ctx context.Context, corpusID string, chunks []Chunk) error {
	if corpusID == "" {
		return cascade.New(cascade.KindInvalidInput, "retrieval: no corpus id")
	}
	for _, c := range chunks {
		if c.ID == "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"retrieval: chunk from %q has no id", c.Path)
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	return x.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		for _, c := range chunks {
			if err := writeDocument(ctx, tx, corpusID, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete retracts chunkIDs and every posting they wrote.
//
// Retraction is driven by the document row's own recorded token list, so
// exactly the postings that were written are the postings that go. A
// posting left behind would keep answering queries with a chunk that no
// longer exists, which is precisely what a retirement is supposed to make
// impossible.
func (x *Index) Delete(ctx context.Context, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	return x.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		for _, id := range chunkIDs {
			if id == "" {
				return cascade.New(cascade.KindInvalidInput, "retrieval: empty chunk id")
			}
			if _, err := retractDocument(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Search runs one query.
//
// The order is the order the guarantee needs: the query is parsed and
// refused if it cannot name a required term, the authorized set is turned
// into a lookup, the postings for the required terms are intersected
// AGAINST that set, and only then is any document content read. Nothing
// is filtered after ranking, because nothing unauthorized ever enters the
// ranking.
func (x *Index) Search(ctx context.Context, req SearchRequest, topK int) ([]Hit, error) {
	if topK <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: topK must be positive, got %d", topK)
	}
	parsed, err := parseQuery(req.Text)
	if err != nil {
		return nil, err
	}
	allowed := allowedSet(req.Scope.RecordIDs)
	if len(allowed) == 0 {
		return []Hit{}, nil
	}
	stats, err := x.corpusStats(ctx, req.Scope.CorpusIDs)
	if err != nil {
		return nil, err
	}
	if stats.Docs == 0 {
		return []Hit{}, nil
	}
	postings, err := x.postingsFor(ctx, parsed.Required, allowed)
	if err != nil {
		return nil, err
	}
	return x.rank(ctx, parsed, postings, stats, topK)
}

// allowedSet turns the authorized record list into a lookup. A nil or
// empty list yields an empty set, which Search treats as "authorized to
// read nothing" — never as "no narrowing requested".
func allowedSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// corpusStats sums the statistics rows of the authorized corpora only. A
// corpus the caller may not read contributes nothing to N or to the
// average document length, so no property of the ranking is a function of
// content outside the caller's scope.
func (x *Index) corpusStats(ctx context.Context, corpusIDs []string) (corpusStats, error) {
	sorted := append([]string(nil), corpusIDs...)
	sort.Strings(sorted)
	var total corpusStats
	for _, id := range sorted {
		s, found, err := readStats(ctx, x.store, id)
		if err != nil {
			return corpusStats{}, err
		}
		if !found {
			continue
		}
		total.Docs += s.Docs
		total.Length += s.Length
	}
	return total, nil
}

// rank reads each surviving candidate, applies the exclusions and phrase
// terms, scores what is left and orders it.
func (x *Index) rank(
	ctx context.Context, q parsedQuery, postings termPostings, stats corpusStats, topK int,
) ([]Hit, error) {
	candidates := postings.intersect(q.Required)
	scored := make([]Hit, 0, len(candidates))
	for _, id := range candidates {
		doc, found, err := readDocument(ctx, x.store, id)
		if err != nil {
			return nil, err
		}
		if !found || !doc.matches(q) {
			continue
		}
		scored = append(scored, Hit{
			ChunkID:  doc.ChunkID,
			Path:     doc.Path,
			CorpusID: doc.CorpusID,
			Snippet:  snippet(doc.Content, q.Required),
			Score:    bm25(q.Required, postings, doc, stats),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].ChunkID < scored[j].ChunkID
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}
