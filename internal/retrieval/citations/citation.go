// Package citations records where a fused retrieval answer actually came
// from, and renders that record for the surfaces that show it.
//
// Purpose: a citation is a claim about provenance, and a wrong claim is
// worse than no claim, because it manufactures confidence in the one place
// a reader has no way to check. Everything here is therefore built to be
// unable to overstate what it knows: a field the source did not supply is
// absent rather than guessed, a merged citation reports the least
// trustworthy of the things it merged, and a result the asking session was
// not authorized to see contributes nothing at all — not a path, not a
// corpus name, not a line number.
//
// Inputs: the fused results produced by internal/retrieval/rrf, in the
// order that package ranked them, plus the scope-resolved record for each
// chunk and, where the source has lines, its line range.
//
// Outputs: a CitationSet in rank order, and its Markdown footnote form.
//
// Constraints: pure computation, no I/O and no network. Nothing here makes
// an authorization decision; it asks the resolver it was handed whether a
// chunk resolves for this session and omits the ones that do not. Nothing
// here enforces trust either: it carries the tag through at its most
// restrictive so a consumer cannot be told a merged row is safer than it
// is.
//
// SPORT: internal.retrieval.citations.Citation/ADDED (P1-E06-W2-S11-T2).
package citations

import (
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

// LineRange is a 1-based, inclusive span of lines in a source file.
//
// The zero value means "this source has no line location", which is a real
// and common state: a memory row or a binary blob has content but no
// lines. It is deliberately not spelled as line 0, and no code here ever
// substitutes a default span for a missing one — a fabricated line range
// is precisely the kind of citation that cannot be caught downstream.
type LineRange struct {
	// Start is the first line of the span, 1-based and inclusive.
	Start int `json:"start"`
	// End is the last line of the span, 1-based and inclusive. A
	// single-line span has End equal to Start.
	End int `json:"end"`
}

// Known reports whether r locates anything. A span is known only when it
// starts at line 1 or later and does not end before it starts; every other
// shape, including the zero value, is an absent location rather than a
// span to be repaired.
func (r LineRange) Known() bool {
	return r.Start >= 1 && r.End >= r.Start
}

// Overlaps reports whether two known spans share at least one line.
// An unknown span overlaps nothing, including another unknown span: two
// citations that both fail to say where they are have not been shown to be
// the same place, and merging them on that basis would invent a fact.
func (r LineRange) Overlaps(o LineRange) bool {
	if !r.Known() || !o.Known() {
		return false
	}
	return r.Start <= o.End && o.Start <= r.End
}

// union returns the smallest span covering both. Callers only reach it for
// spans that overlap, so the result never spans a gap neither citation
// covered.
func (r LineRange) union(o LineRange) LineRange {
	out := r
	if o.Start < out.Start {
		out.Start = o.Start
	}
	if o.End > out.End {
		out.End = o.End
	}
	return out
}

// Citation is the provenance of one fused result: where the content came
// from, how strongly it ranked, and which legs found it.
//
// Every location field is optional at the record level. A result whose
// source has no path carries no path; a source with no lines carries no
// line range. The absence is the honest answer and is rendered as such.
type Citation struct {
	// ChunkID is the stable, content-addressed id of the chunk whose rank
	// and score this citation reports. After a same-file merge it is the
	// id of the strongest contributor, and the others are in
	// MergedChunkIDs.
	ChunkID string `json:"chunk_id"`
	// MergedChunkIDs are the other chunk ids folded into this citation by
	// the same-file overlapping-range dedupe, sorted and never containing
	// ChunkID. Nil when nothing was merged, so the common case reads as
	// what it is: one citation, one chunk.
	MergedChunkIDs []string `json:"merged_chunk_ids,omitempty"`
	// Path is the source path the fused result was carved from, exactly
	// as fusion resolved it for this chunk id. Empty when the source has
	// no path.
	Path string `json:"path,omitempty"`
	// Lines is the 1-based span within Path, or the zero LineRange when
	// the source has no line location.
	Lines LineRange `json:"lines"`
	// CorpusID names the corpus the authorized record belongs to. It is
	// taken from the record the resolver returned rather than from the
	// leg's own claim, because the resolver's answer is the one that was
	// actually authorized.
	CorpusID string `json:"corpus_id,omitempty"`
	// Trust is the effective TRUST tag: the least trusted of everything
	// that went into this citation. It never reports a tag more
	// permissive than any of its inputs.
	Trust corpus.TrustLevel `json:"trust"`
	// Rank is the 1-based position of the result in the fused ranking.
	// After a merge it is the strongest (lowest) rank merged.
	Rank int `json:"rank"`
	// Score is the fused result's normalized score in [0,1]. After a
	// merge it is the highest score merged, matching Rank.
	Score float64 `json:"score"`
	// RawScore is the pre-normalization RRF score behind Score, kept so a
	// consumer can compare citations across two different result sets,
	// where the normalized scores are not comparable.
	RawScore float64 `json:"raw_score"`
	// Strategies are the retrieval legs that contributed, sorted by name.
	// After a merge it is the union of the merged citations' legs.
	Strategies []rrf.StrategyName `json:"strategies,omitempty"`
}

// trustRank orders TRUST levels from least to most trusted so combining
// them restrictively is a comparison rather than a special case. An
// undefined value ranks below the lowest defined level, which is what
// makes an unreadable tag resolve to untrusted instead of to trusted.
// It mirrors the corpus package's own unexported ordering; the two are
// asserted to agree by TestLeastTrustAgreesWithTheCorpusModel rather than by
// exporting an internal of the corpus model.
func trustRank(t corpus.TrustLevel) int {
	switch t {
	case corpus.TrustUntrustedSource:
		return 1
	case corpus.TrustTrusted:
		return 2
	default:
		return 0
	}
}

// leastTrust returns the less trusted of a and b, collapsing anything that
// is not a defined level to corpus.TrustUntrustedSource.
//
// This is the whole anti-laundering rule in one function. Fusion already
// resolves a chunk that two differently classified paths reported to the
// untrusted side; if a citation then re-derived trust from the authorized
// record alone, the trusted half of that pair would reappear in the
// citation and the merge would read as safe. Combining restrictively at
// every step makes that impossible to express.
func leastTrust(a, b corpus.TrustLevel) corpus.TrustLevel {
	ra, rb := trustRank(a), trustRank(b)
	if ra == 0 || rb == 0 {
		return corpus.TrustUntrustedSource
	}
	if ra <= rb {
		return a
	}
	return b
}

// mergeable reports whether o describes the same region of the same file
// as c, which is the only condition under which two citations may become
// one. Both must name the same non-empty path and both must carry known,
// overlapping spans. Two citations with no line information never merge,
// however similar they look.
func (c Citation) mergeable(o Citation) bool {
	if c.Path == "" || c.Path != o.Path {
		return false
	}
	return c.Lines.Overlaps(o.Lines)
}

// mergeStrategies returns the sorted union of two strategy lists. Sorting
// the union rather than appending keeps the field a function of the
// citations' values and not of the order they happened to be merged in.
func mergeStrategies(a, b []rrf.StrategyName) []rrf.StrategyName {
	seen := make(map[rrf.StrategyName]bool, len(a)+len(b))
	out := make([]rrf.StrategyName, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// mergeChunkIDs returns the sorted set of every chunk id in both
// citations except primary, which is reported separately as the merged
// citation's ChunkID.
func mergeChunkIDs(primary string, cs ...Citation) []string {
	seen := map[string]bool{primary: true}
	var out []string
	for _, c := range cs {
		for _, id := range append([]string{c.ChunkID}, c.MergedChunkIDs...) {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// merge folds o into c and returns the single citation covering both.
//
// The stronger half — the one that ranked better — supplies the identity,
// rank and scores, so the citation's numbers still describe a real result
// rather than an average of two. Everything else combines conservatively:
// the span is the union, the strategies are the union, and the trust is
// the lesser.
func (c Citation) merge(o Citation) Citation {
	strong, weak := c, o
	if weak.Rank < strong.Rank {
		strong, weak = o, c
	}
	out := strong
	out.Lines = c.Lines.union(o.Lines)
	out.Strategies = mergeStrategies(c.Strategies, o.Strategies)
	out.Trust = leastTrust(c.Trust, o.Trust)
	out.MergedChunkIDs = mergeChunkIDs(strong.ChunkID, weak, strong)
	if weak.Score > out.Score {
		out.Score = weak.Score
	}
	if weak.RawScore > out.RawScore {
		out.RawScore = weak.RawScore
	}
	return out
}
