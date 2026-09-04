// Purpose: CitationSet and Assemble — turning one fused result list into
// the provenance record for that answer, with the citation-level dedupe
// that collapses two overlapping citations into the same file into one.
//
// Inputs: the fused results in rank order, a resolver that says which
// chunks the asking session is authorized to see, and an optional locator
// that supplies line spans.
//
// Outputs: a CitationSet in rank order, or a taxonomy error.
//
// Constraints: the output is a function of the input values. Grouping is
// done by scanning the citations built so far rather than through a map,
// so no part of the ordering can come from map iteration; the final sort
// breaks rank ties on the chunk id, so no two citations are ever
// interchangeable. A result the resolver does not authorize contributes
// nothing anywhere, including to the counts a caller might render.
//
// SPORT: internal.retrieval.citations.Assemble/ADDED (P1-E06-W2-S11-T2).

package citations

import (
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SourceResolver answers whether a chunk id resolves to a record the
// asking session is authorized to see, and returns that record.
//
// It is exactly the shape of the query path's scope filter
// (internal/retrieval/fusion.ScopeFilter.Resolve), declared here as a
// one-method interface so this package depends on the question rather than
// on the filter. It is never a second scope decision: the answer is the
// one the corpus model already took, being consulted again because a
// citation is a disclosure and must not outlive the authorization behind
// the result it describes.
type SourceResolver interface {
	// Resolve returns the authorized record for chunkID, and false when
	// the session has no authorized record for it.
	Resolve(chunkID string) (corpus.Record, bool)
}

// LineLocator supplies the 1-based line span a chunk occupies in its
// source file, for the sources that have lines.
//
// It is optional. A nil locator means no citation carries a line range,
// which is an honest "not known here" — never a reason to invent one, and
// never an error.
type LineLocator interface {
	// Lines returns the span chunkID occupies, and false when the source
	// has no line location.
	Lines(chunkID string) (LineRange, bool)
}

// Options are Assemble's inputs beyond the results themselves.
type Options struct {
	// Resolver decides which results may be cited. It is REQUIRED:
	// assembling citations without knowing what the session may see is
	// the failure this package exists to prevent, so a nil Resolver is
	// refused rather than defaulted to "cite everything".
	Resolver SourceResolver
	// Locator supplies line spans. Optional; nil means no line ranges.
	Locator LineLocator
}

// CitationSet is the provenance of one fused answer.
type CitationSet struct {
	// Citations are the citations in rank order, ties broken by chunk id.
	// Empty for an answer with nothing to cite.
	Citations []Citation `json:"citations"`
	// Withheld counts the fused results that produced no citation because
	// the resolver did not authorize them. It is a count and nothing
	// else: a withheld result's path, corpus and location never appear
	// anywhere in this value or in its rendered form.
	Withheld int `json:"withheld"`
}

// Len returns the number of citations in the set.
func (s CitationSet) Len() int { return len(s.Citations) }

// Assemble builds the citation set for results, which must be the fused
// results in the order internal/retrieval/rrf ranked them: a citation's
// Rank is its position in that list, so handing them over re-sorted would
// silently mis-rank every citation.
//
// A nil results slice is a caller bug and is refused. An EMPTY results
// slice is a query that found nothing, which is an ordinary outcome and
// returns an empty set with no error — an answer with no sources is
// correctly cited by citing nothing.
//
// Results the resolver does not authorize are dropped and counted in
// Withheld. So are results whose leg claimed a corpus the authorized
// record does not agree with: the disagreement means the leg reached the
// chunk through a corpus the session was not cleared for, and the
// fail-closed reading of a disagreement about authorization is to withhold.
func Assemble(results []rrf.FusedResult, opts Options) (CitationSet, error) {
	if results == nil {
		return CitationSet{}, cascade.New(cascade.KindInvalidInput,
			"citations: nil fused-result input")
	}
	if opts.Resolver == nil {
		return CitationSet{}, cascade.New(cascade.KindInvalidInput,
			"citations: no source resolver; citations cannot be assembled without one")
	}
	set := CitationSet{}
	for i, r := range results {
		c, ok, err := citationFor(r, i+1, opts)
		if err != nil {
			return CitationSet{}, err
		}
		if !ok {
			set.Withheld++
			continue
		}
		merged, err := foldInto(set.Citations, c)
		if err != nil {
			return CitationSet{}, err
		}
		set.Citations = merged
	}
	sortCitations(set.Citations)
	return set, nil
}

// citationFor builds the citation for one fused result at rank, or reports
// that the result must not be cited.
//
// The identity is taken from the fused result itself — its chunk id, and
// the path fusion deterministically chose for that chunk id — so the
// citation points at the row it is attached to even where fusion collapsed
// several source lists into that row. Deriving the path from anywhere else
// is how a citation ends up naming the wrong one of two merged sources.
func citationFor(r rrf.FusedResult, rank int, opts Options) (Citation, bool, error) {
	if r.ChunkID == "" {
		return Citation{}, false, cascade.Newf(cascade.KindInvalidInput,
			"citations: fused result at rank %d has no chunk id", rank)
	}
	rec, ok := opts.Resolver.Resolve(r.ChunkID)
	if !ok {
		return Citation{}, false, nil
	}
	if r.CorpusID != "" && rec.CorpusID != r.CorpusID {
		return Citation{}, false, nil
	}
	c := Citation{
		ChunkID:    r.ChunkID,
		Path:       r.Path,
		CorpusID:   rec.CorpusID,
		Trust:      leastTrust(r.Trust, rec.Trust),
		Rank:       rank,
		Score:      r.Score,
		RawScore:   r.RawScore,
		Strategies: append([]rrf.StrategyName(nil), r.Strategies...),
	}
	if opts.Locator != nil {
		if lines, has := opts.Locator.Lines(r.ChunkID); has && lines.Known() {
			c.Lines = lines
		}
	}
	return c, true, nil
}

// foldInto adds c to the citations built so far, merging it into the first
// citation describing the same region of the same file.
//
// The scan is over a slice in construction order rather than a lookup in a
// map keyed by path, because the merged citation's identity depends on
// which citation it met first and a map would make that dependence an
// iteration-order dependence.
func foldInto(existing []Citation, c Citation) ([]Citation, error) {
	for i := range existing {
		if !existing[i].mergeable(c) {
			continue
		}
		if existing[i].CorpusID != c.CorpusID {
			return nil, cascade.Newf(cascade.KindConflict,
				"citations: %s lines %d-%d is claimed by corpus %s and corpus %s; "+
					"merging would attribute content to a corpus it did not come from",
				c.Path, c.Lines.Start, c.Lines.End, existing[i].CorpusID, c.CorpusID)
		}
		existing[i] = existing[i].merge(c)
		return existing, nil
	}
	return append(existing, c), nil
}

// sortCitations orders the set: ascending rank, then ascending chunk id.
// Ranks are unique across the fused list and a merge keeps the lower of
// two, so the comparison is already a total order; the chunk-id tie-break
// is there so it stays one if a future caller ever hands in a list where
// it is not.
func sortCitations(cs []Citation) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Rank != cs[j].Rank {
			return cs[i].Rank < cs[j].Rank
		}
		return cs[i].ChunkID < cs[j].ChunkID
	})
}
