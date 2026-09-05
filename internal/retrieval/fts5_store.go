package retrieval

// Purpose: the full-text index's key-value access layer — reading and
//   writing document rows, postings and the per-corpus statistics row,
//   plus the posting intersection the query path narrows candidates with.
//   Split from fts5.go under the Art.10.3 300-line file cap; it is this
//   index's storage concern rather than a mechanical relocation.
// Inputs: a provider.Store or a provider.Tx, and the keys fts5_schema.go
//   lays out.
// Outputs: decoded rows, candidate id lists, or a pkg/cascade taxonomy
//   error.
// Constraints: every write that touches a document row also touches that
//   row's postings and its corpus statistics, inside the caller's single
//   transaction, so the three can never disagree. Reads never widen: the
//   authorized set is applied to a posting scan as it is consumed, so an
//   unauthorized id is dropped before it is ever a candidate.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"context"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// readDocument returns the row for chunkID, reporting whether one exists.
func readDocument(ctx context.Context, s provider.Store, chunkID string) (document, bool, error) {
	data, err := s.Get(ctx, IndexNamespace, docKey(chunkID))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return document{}, false, nil
		}
		return document{}, false, cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: reading index row %s", chunkID)
	}
	doc, err := decodeDocument(data)
	return doc, err == nil, err
}

// readStats returns corpusID's statistics row, reporting whether one
// exists. A corpus with no row has never been indexed, which is not an
// error: it contributes nothing to the ranking's aggregates.
func readStats(ctx context.Context, s provider.Store, corpusID string) (corpusStats, bool, error) {
	data, err := s.Get(ctx, IndexNamespace, statKey(corpusID))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return corpusStats{}, false, nil
		}
		return corpusStats{}, false, cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: reading corpus statistics for %q", corpusID)
	}
	st, err := decodeStats(data)
	return st, err == nil, err
}

// writeDocument indexes one chunk, retracting whatever the same chunk id
// held before. That retraction is what makes the write idempotent: a
// second write of the same chunk removes the first one's postings and
// statistics contribution before adding its own, so the row count and the
// posting count are both exactly one.
func writeDocument(ctx context.Context, tx provider.Tx, corpusID string, c Chunk) error {
	if _, err := retractDocument(ctx, tx, c.ID); err != nil {
		return err
	}
	tokens, counts, length := tokenCounts(string(c.Content))
	doc := document{
		ChunkID: c.ID, Path: c.Path, CorpusID: corpusID, Lang: c.Lang,
		Content: string(c.Content), Tokens: tokens, Frequencies: counts, Length: length,
	}
	data, err := encodeDocument(doc)
	if err != nil {
		return err
	}
	if err := tx.Put(ctx, IndexNamespace, docKey(doc.ChunkID), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "retrieval: writing index row %s", doc.ChunkID)
	}
	for i, tok := range tokens {
		if err := tx.Put(ctx, IndexNamespace, tokenKey(tok, doc.ChunkID), encodeFrequency(counts[i])); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "retrieval: writing posting %q", tok)
		}
	}
	return adjustStats(ctx, tx, corpusID, 1, int64(length))
}

// retractDocument removes a chunk's row, its postings and its statistics
// contribution, reporting whether there was anything to remove.
func retractDocument(ctx context.Context, tx provider.Tx, chunkID string) (bool, error) {
	data, err := tx.Get(ctx, IndexNamespace, docKey(chunkID))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: reading index row %s", chunkID)
	}
	doc, err := decodeDocument(data)
	if err != nil {
		return false, err
	}
	for _, tok := range doc.Tokens {
		if derr := tx.Delete(ctx, IndexNamespace, tokenKey(tok, chunkID)); derr != nil {
			return false, cascade.Wrapf(cascade.KindUnavailable, derr,
				"retrieval: retracting posting %q", tok)
		}
	}
	if derr := tx.Delete(ctx, IndexNamespace, docKey(chunkID)); derr != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, derr,
			"retrieval: deleting index row %s", chunkID)
	}
	return true, adjustStats(ctx, tx, doc.CorpusID, -1, -int64(doc.Length))
}

// adjustStats applies a delta to a corpus's statistics row inside the
// caller's transaction. The row is clamped at zero rather than allowed
// negative: a negative document count would make the ranking's inverse
// document frequency meaningless, and clamping keeps a scoring bug from
// becoming an arithmetic one.
func adjustStats(ctx context.Context, tx provider.Tx, corpusID string, docs, length int64) error {
	var st corpusStats
	data, err := tx.Get(ctx, IndexNamespace, statKey(corpusID))
	switch {
	case err == nil:
		if st, err = decodeStats(data); err != nil {
			return err
		}
	case !cascade.HasKind(err, cascade.KindNotFound):
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: reading corpus statistics for %q", corpusID)
	}
	st.Docs = clampZero(st.Docs + docs)
	st.Length = clampZero(st.Length + length)
	encoded, err := encodeStats(st)
	if err != nil {
		return err
	}
	if err := tx.Put(ctx, IndexNamespace, statKey(corpusID), encoded); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: writing corpus statistics for %q", corpusID)
	}
	return nil
}

func clampZero(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// termPostings is the authorized posting list per required term: for each
// term, the term frequency of every AUTHORIZED document carrying it. The
// document frequency the ranking uses is len of one of these lists, so it
// too is computed over the caller's own scope and not over the index.
type termPostings map[string]map[string]int

// postingsFor scans the postings of every required term, keeping only
// documents in allowed.
func (x *Index) postingsFor(
	ctx context.Context, terms []string, allowed map[string]bool,
) (termPostings, error) {
	out := make(termPostings, len(terms))
	for _, term := range terms {
		hits, err := x.scanTerm(ctx, term, allowed)
		if err != nil {
			return nil, err
		}
		out[term] = hits
		if len(hits) == 0 {
			// A required term nothing authorized carries makes the
			// conjunction empty; the remaining scans cannot change that.
			return out, nil
		}
	}
	return out, nil
}

// scanTerm reads one term's authorized postings.
func (x *Index) scanTerm(
	ctx context.Context, term string, allowed map[string]bool,
) (map[string]int, error) {
	prefix := tokenScanPrefix(term)
	it, err := x.store.Scan(ctx, IndexNamespace, prefix)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "retrieval: scanning postings for %q", term)
	}
	defer func() { _ = it.Close() }()
	hits := make(map[string]int)
	for it.Next(ctx) {
		id := strings.TrimPrefix(it.Key(), prefix)
		if !allowed[id] {
			continue
		}
		freq, ferr := decodeFrequency(it.Value())
		if ferr != nil {
			return nil, ferr
		}
		hits[id] = freq
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "retrieval: scanning postings for %q", term)
	}
	return hits, nil
}

// intersect returns the sorted ids carrying EVERY required term.
//
// Conjunction is the reading that cannot return more than the caller asked
// for, and starting the accumulator from the first term's postings rather
// than from the authorized set means the walk begins narrow.
func (p termPostings) intersect(required []string) []string {
	var acc map[string]bool
	for i, term := range required {
		hits := p[term]
		if len(hits) == 0 {
			return nil
		}
		if i == 0 {
			acc = make(map[string]bool, len(hits))
			for id := range hits {
				acc[id] = true
			}
			continue
		}
		for id := range acc {
			if _, ok := hits[id]; !ok {
				delete(acc, id)
			}
		}
		if len(acc) == 0 {
			return nil
		}
	}
	out := make([]string, 0, len(acc))
	for id := range acc {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
