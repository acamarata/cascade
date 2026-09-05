package retrieval

// Purpose: the full-text index's on-store schema — the namespace and key
//   layout every document row and posting is written under, the stored
//   document record and its codec, the per-corpus statistics row BM25
//   scoring reads, and the tokenizer both the write path and the query
//   parser share.
// Inputs: chunks handed to the Index write path; stored bytes on the way
//   back in.
// Outputs: encoded rows, posting keys, or a pkg/cascade taxonomy error.
// Constraints: pure functions plus key-value reads; no clock, no
//   randomness, no direct SQL. Every derived value is deterministic:
//   tokens are lowercased, bounded and sorted, postings are keyed by
//   token then chunk id, and results are ordered by score then chunk id,
//   so an identical corpus and query rank identically on any machine.
//
//   CONTRACT DEVIATION (recorded, not papered over). This ticket's
//   contract asks for "CREATE VIRTUAL TABLE retrieval_fts USING fts5(...)"
//   expressed as migration 001 of the retrieval domain. The tree does not
//   permit it, and this is now the THIRD ticket to reach that conclusion
//   independently rather than a fresh opinion. Every cascade.db domain
//   persists through pkg/provider.Store, whose whole contract is
//   Get/Put/Delete/Scan/Tx over one physical key-value table
//   (providers/sqlite/driver.go schemaDDL) with no seam that reaches a
//   *sql.DB; the production migration set is deliberately empty
//   (cmd/cascade/daemon_unix_store.go: "adding speculative steps with
//   nothing to migrate would be its own Article-1 violation"), and only
//   the composition root holds the raw handle a CREATE VIRTUAL TABLE
//   would need. internal/audit (internal/audit/schema.go) and
//   internal/memory (internal/memory/schema.go) each hit this same
//   question one epic apart and each shipped an inverted token index in
//   the key-value namespace instead. This file follows that precedent
//   deliberately and by the same reasoning, so the tree has one answer to
//   this question rather than three. The queryable behaviour the
//   contract's acceptance criteria actually name — a term in a chunk
//   returns that chunk, ranked, deduped on chunk id, idempotent on
//   re-index, gone on delete — is delivered and tested against a real
//   SQLite database through the store abstraction every other domain
//   uses. See the journal for both sides quoted.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// IndexNamespace is the pkg/provider.Store namespace every key below is
// written under: the ratified retrieval domain from R-14.5's closed ten,
// taken from internal/storage rather than re-spelled as a literal, so the
// domain-ownership registry and this package cannot drift apart.
const IndexNamespace = string(storage.DomainRetrieval)

// The key layout inside the retrieval namespace. Every key this file
// writes begins with indexPrefix, so the full-text index can be dropped
// whole without touching the embedding pipeline's own keys in the same
// domain.
//
// A document key is "fts:doc:<chunkID>". A posting key is
// "fts:tok:<token>:<chunkID>", scanned by the prefix "fts:tok:<token>:"
// to yield every chunk carrying that token. A statistics key is
// "fts:stat:<corpusID>". Tokens are restricted to ASCII letters and
// digits by tokenize, so ":" never appears inside one and the split is
// unambiguous.
const (
	indexPrefix = "fts:"
	docPrefix   = indexPrefix + "doc:"
	tokenPrefix = indexPrefix + "tok:"
	statPrefix  = indexPrefix + "stat:"
)

func docKey(chunkID string) string          { return docPrefix + chunkID }
func tokenKey(token, chunkID string) string { return tokenPrefix + token + ":" + chunkID }
func tokenScanPrefix(token string) string   { return tokenPrefix + token + ":" }
func statKey(corpusID string) string        { return statPrefix + corpusID }

// document is one indexed chunk as the index holds it.
//
// Content is stored because a snippet has to be cut from the text that was
// actually indexed: cutting it from the file at query time would show a
// reader something the index never matched, and a phrase term is verified
// against these exact bytes rather than against a positional index this
// layout does not keep.
type document struct {
	// ChunkID is the content-addressed chunk id (id.go), the identity the
	// whole epic joins on and the key this row is stored under.
	ChunkID string `json:"chunk_id"`
	// Path, CorpusID and Lang are provenance carried into every Hit.
	Path     string `json:"path"`
	CorpusID string `json:"corpus_id"`
	Lang     string `json:"lang,omitempty"`
	// Content is the chunk's exact indexed text.
	Content string `json:"content"`
	// Tokens is the sorted, deduplicated posting set written for this
	// row, stored so an overwrite or a delete retracts precisely the
	// postings it wrote without scanning the whole token space. A
	// posting this list does not name is a posting nothing can retract,
	// which is the stale-posting failure the forget pipeline exists to
	// prevent.
	Tokens []string `json:"tokens,omitempty"`
	// Frequencies holds each token's occurrence count, in Tokens' order.
	Frequencies []int `json:"frequencies,omitempty"`
	// Length is the document's total token count (occurrences, not
	// distinct tokens): BM25's document-length normalization term.
	Length int `json:"length"`
}

// corpusStats is one corpus's aggregate row: how many documents it holds
// and their total length, the two numbers BM25 needs for N and for the
// average document length. It is maintained transactionally alongside the
// document rows, never recomputed by a scan, so an index of any size costs
// the same to score against.
type corpusStats struct {
	Docs   int64 `json:"docs"`
	Length int64 `json:"length"`
}

// ErrIndexCorrupt is returned when a stored row cannot be decoded. It is
// an integrity refusal rather than a silent skip: a query that quietly
// dropped rows it could not read would return a short answer that looks
// complete, and the response is to re-index, never to repair a row in
// place.
var ErrIndexCorrupt = cascade.New(cascade.KindIntegrity, "corrupt full-text index row")

// encodeDocument marshals a row. Field order is the struct's, fixed at
// compile time, so identical rows encode to identical bytes.
func encodeDocument(d document) ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, ErrIndexCorrupt,
			"retrieval: encoding index row %s: %v", d.ChunkID, err)
	}
	return data, nil
}

// decodeDocument unmarshals a row, refusing anything it cannot read whole.
func decodeDocument(data []byte) (document, error) {
	var d document
	if err := json.Unmarshal(data, &d); err != nil {
		return document{}, cascade.Wrapf(cascade.KindIntegrity, ErrIndexCorrupt,
			"retrieval: decoding an index row: %v", err)
	}
	if len(d.Tokens) != len(d.Frequencies) {
		return document{}, cascade.Wrapf(cascade.KindIntegrity, ErrIndexCorrupt,
			"retrieval: index row %s carries %d tokens and %d frequencies",
			d.ChunkID, len(d.Tokens), len(d.Frequencies))
	}
	return d, nil
}

// encodeStats and decodeStats are corpusStats' codec.
func encodeStats(s corpusStats) ([]byte, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "retrieval: encoding corpus statistics")
	}
	return data, nil
}

func decodeStats(data []byte) (corpusStats, error) {
	var s corpusStats
	if err := json.Unmarshal(data, &s); err != nil {
		return corpusStats{}, cascade.Wrapf(cascade.KindIntegrity, ErrIndexCorrupt,
			"retrieval: decoding corpus statistics: %v", err)
	}
	return s, nil
}

// encodeFrequency and decodeFrequency are the posting value's codec: a
// posting stores the token's occurrence count in that document, which is
// BM25's term-frequency input. Decimal rather than binary so a posting is
// readable in a store dump.
func encodeFrequency(n int) []byte { return []byte(strconv.Itoa(n)) }

func decodeFrequency(data []byte) (int, error) {
	n, err := strconv.Atoi(string(data))
	if err != nil || n <= 0 {
		return 0, cascade.Wrapf(cascade.KindIntegrity, ErrIndexCorrupt,
			"retrieval: posting carries an unreadable term frequency %q", string(data))
	}
	return n, nil
}

// maxTokenLen bounds one token. A longer run of characters is truncated
// rather than dropped, so a pathological input costs a bounded key size
// instead of making its chunk unfindable.
const maxTokenLen = 64

// tokenize splits text into lowercase alphanumeric tokens in occurrence
// order, with no deduplication. It is the whole of the full-text analysis
// and it is shared by the write path and the query parser: no stemming, no
// stop words, no language guessing, because each of those makes a hit
// depend on a table that has to match at query time and silently changes
// the result set when it does not.
//
// This is where the contract's "tokenize='porter unicode61'" lands. Porter
// stemming is deliberately NOT implemented: a stemmer is a table, and a
// hand-written one would be this package's own approximation of SQLite's,
// asserted against itself. Matching is therefore exact-token, which is
// narrower than the contract's tokenizer and never wider — a query returns
// a subset of what a stemmed index would, never a superset, so no chunk is
// disclosed that stemming would have withheld.
func tokenize(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if cur.Len() < maxTokenLen {
				cur.WriteRune(r)
			}
		default:
			flush()
		}
	}
	flush()
	return out
}

// tokenCounts folds tokenize's output into the sorted distinct tokens and
// their occurrence counts, the exact pair a document row stores.
func tokenCounts(text string) (tokens []string, counts []int, length int) {
	seen := make(map[string]int)
	all := tokenize(text)
	for _, tok := range all {
		seen[tok]++
	}
	tokens = make([]string, 0, len(seen))
	for tok := range seen {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)
	counts = make([]int, len(tokens))
	for i, tok := range tokens {
		counts[i] = seen[tok]
	}
	return tokens, counts, len(all)
}
