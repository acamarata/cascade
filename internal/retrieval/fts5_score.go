package retrieval

// Purpose: BM25 ranking and snippet extraction, plus the document-side
//   predicates the query's exclusions and phrase terms are checked with.
//   Split from fts5.go under the Art.10.3 300-line file cap.
// Inputs: a parsed query, the authorized postings, one document row and
//   the authorized corpora's aggregate statistics.
// Outputs: a relevance score and a bounded snippet.
// Constraints: pure functions, no I/O. Every input is already scoped: the
//   postings were intersected against the authorized set and the
//   statistics were summed over authorized corpora only, so no term of the
//   score is a function of content the caller may not read. Summation runs
//   over the query's sorted term list, so the floating-point result is a
//   function of the inputs alone and two runs produce the same bits.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"math"
	"strings"
)

// BM25 tuning. These are the values the reference formulation uses and
// SQLite's own FTS5 rank function ships with; they are named rather than
// spelled at the call site so the ranking has exactly one place to tune.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// snippetMaxRunes bounds a snippet. It is a rune bound, and the cut lands
// on a rune boundary, so a snippet is never half a character.
const snippetMaxRunes = 200

// matches reports whether doc survives the query's non-postings terms:
// every excluded token absent, every phrase present as a real adjacency.
//
// These are checked here, after the postings intersection, rather than
// folded into it: an exclusion has no posting list of its own to
// intersect, and a phrase needs the document's token ORDER, which the
// postings deliberately do not keep.
func (d document) matches(q parsedQuery) bool {
	present := make(map[string]bool, len(d.Tokens))
	for _, t := range d.Tokens {
		present[t] = true
	}
	for _, t := range q.Excluded {
		if present[t] {
			return false
		}
	}
	if len(q.Phrases) == 0 {
		return true
	}
	stream := " " + strings.Join(tokenize(d.Content), " ") + " "
	for _, phrase := range q.Phrases {
		if !strings.Contains(stream, " "+phrase+" ") {
			return false
		}
	}
	return true
}

// bm25 scores doc against the query's required terms.
//
// The document frequency of each term is the size of its AUTHORIZED
// posting list, and N and the average length come from the authorized
// corpora, so the score is computed entirely within the caller's scope.
// A document the statistics have not caught up with cannot make the
// length normalization divide by zero: avgLength falls back to the
// document's own length, and then to 1.
func bm25(required []string, postings termPostings, doc document, stats corpusStats) float64 {
	avg := avgLength(stats, doc)
	norm := bm25K1 * (1 - bm25B + bm25B*float64(doc.Length)/avg)
	var score float64
	for _, term := range required {
		hits := postings[term]
		tf := float64(hits[doc.ChunkID])
		if tf == 0 {
			continue
		}
		score += idf(int64(len(hits)), stats.Docs) * (tf * (bm25K1 + 1)) / (tf + norm)
	}
	return score
}

// idf is the BM25 inverse document frequency, in the "plus one" form that
// never goes negative: a term carried by most of the corpus contributes
// little, never a penalty that could rank a matching document below a
// non-matching one.
func idf(df, n int64) float64 {
	if df <= 0 || n <= 0 {
		return 0
	}
	if df > n {
		// The statistics row lags the postings (a corpus row that has
		// not caught up). Clamping keeps the logarithm's argument above
		// one rather than letting a bookkeeping gap produce a negative
		// weight that would invert the ranking.
		df = n
	}
	return math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
}

// avgLength returns the average document length to normalize against.
func avgLength(stats corpusStats, doc document) float64 {
	if stats.Docs > 0 && stats.Length > 0 {
		return float64(stats.Length) / float64(stats.Docs)
	}
	if doc.Length > 0 {
		return float64(doc.Length)
	}
	return 1
}

// snippet returns a bounded window of content around the first required
// term that occurs in it, or the leading window when none does.
//
// The window is cut from the INDEXED bytes, not re-read from the file: a
// snippet taken from a file that has changed since indexing would show a
// reader text the index never matched, which is worse than showing them
// nothing.
func snippet(content string, required []string) string {
	runes := []rune(content)
	start := snippetStart(content, runes, required)
	end := start + snippetMaxRunes
	if end > len(runes) {
		end = len(runes)
	}
	return strings.TrimSpace(string(runes[start:end]))
}

// snippetStart picks the window's first rune: the start of the earliest
// occurrence of any required term, scanned in the query's own sorted term
// order so the choice does not depend on map iteration.
func snippetStart(content string, runes []rune, required []string) int {
	lower := strings.ToLower(content)
	best := -1
	for _, term := range required {
		at := strings.Index(lower, term)
		if at < 0 {
			continue
		}
		if best < 0 || at < best {
			best = at
		}
	}
	if best <= 0 {
		return 0
	}
	// Convert the byte offset to a rune offset and back off a little so
	// the matched term has some context in front of it.
	at := len([]rune(content[:best]))
	if at > snippetMaxRunes/4 {
		at -= snippetMaxRunes / 4
	} else {
		at = 0
	}
	if at > len(runes) {
		at = len(runes)
	}
	return at
}
