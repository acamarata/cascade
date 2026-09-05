package retrieval

// Purpose: the query parser. A user's raw search string arrives here
//   unvalidated, from a CLI flag or an RPC field, and this file is the
//   only thing between it and the index.
// Inputs: an arbitrary query string.
// Outputs: a parsedQuery, or a pkg/cascade KindInvalidInput refusal.
// Constraints: FAILS CLOSED, without exception. Every path that cannot
//   produce at least one REQUIRED term refuses. That is the whole point of
//   the file: a parser that failed open would answer "-nothing" or "!!!"
//   with the entire authorized corpus, which is both a wrong answer and a
//   disclosure of every chunk the caller never asked to see. Bounds are
//   enforced before any work is done, so a pathological query costs a
//   rejection rather than a scan.
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Query-shape bounds. They are refusals, not truncations: silently
// dropping the tail of a query answers a question the caller did not ask.
const (
	// maxQueryBytes bounds the raw string.
	maxQueryBytes = 4096
	// maxQueryTerms bounds the total number of terms, required, excluded
	// and phrase together.
	maxQueryTerms = 32
)

// parsedQuery is one accepted query.
//
// Required is never empty in a value this package produces: an accepted
// query always names at least one term a document must carry, so the
// candidate set always starts from a posting scan and never from "every
// document".
type parsedQuery struct {
	// Required tokens a document must all carry, sorted and deduplicated.
	Required []string
	// Excluded tokens a document must not carry, sorted and deduplicated.
	Excluded []string
	// Phrases are quoted runs. Each is the phrase's tokens joined by a
	// single space, matched against the document's own tokens joined the
	// same way, so a phrase is a real adjacency test rather than a
	// conjunction wearing quotes. Every phrase token is also in Required,
	// which is what narrows the candidate set before any content is read.
	Phrases []string
}

// parseQuery accepts the query language and refuses everything else.
//
// The language is small on purpose: bare terms are required (conjunctive),
// a leading "-" excludes, and "a b" in double quotes requires those tokens
// adjacent in that order. There is no OR, no wildcard and no nesting,
// because each of those widens a result set and every widening operator is
// one more way for a malformed query to reach content the caller is not
// asking about.
func parseQuery(raw string) (parsedQuery, error) {
	if strings.TrimSpace(raw) == "" {
		return parsedQuery{}, cascade.New(cascade.KindInvalidInput,
			"retrieval: empty query")
	}
	if len(raw) > maxQueryBytes {
		return parsedQuery{}, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: query is %d bytes, over the %d-byte limit", len(raw), maxQueryBytes)
	}
	fields, err := splitQuery(raw)
	if err != nil {
		return parsedQuery{}, err
	}
	return buildQuery(fields)
}

// queryField is one lexed term with its operator.
type queryField struct {
	text     string
	negated  bool
	isPhrase bool
}

// splitQuery lexes raw into fields, honouring double-quoted runs.
//
// An unterminated quote is refused rather than closed at end of input:
// closing it silently turns `foo "bar` into a query the caller did not
// write, and the reading that seems harmless here is the reading that
// makes a typo return a different document set without saying so.
func splitQuery(raw string) ([]queryField, error) {
	var out []queryField
	var cur strings.Builder
	negated, inQuote, quoted := false, false, false
	flush := func() {
		if cur.Len() > 0 || quoted {
			out = append(out, queryField{text: cur.String(), negated: negated, isPhrase: quoted})
		}
		cur.Reset()
		negated, quoted = false, false
	}
	for _, r := range raw {
		switch {
		case r == '"':
			if inQuote {
				inQuote, quoted = false, true
				flush()
				continue
			}
			inQuote, quoted = true, true
		case inQuote:
			cur.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r == '-' && cur.Len() == 0 && !negated:
			negated = true
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, cascade.New(cascade.KindInvalidInput,
			"retrieval: query has an unterminated quote")
	}
	flush()
	return out, nil
}

// buildQuery turns lexed fields into a parsedQuery, refusing every shape
// that would leave the required set empty.
func buildQuery(fields []queryField) (parsedQuery, error) {
	if len(fields) > maxQueryTerms {
		return parsedQuery{}, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: query has %d terms, over the %d-term limit", len(fields), maxQueryTerms)
	}
	req, exc, phr := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range fields {
		if err := addField(f, req, exc, phr); err != nil {
			return parsedQuery{}, err
		}
	}
	if len(req) == 0 {
		return parsedQuery{}, cascade.New(cascade.KindInvalidInput,
			"retrieval: query names no searchable term; a query that matched "+
				"everything would return the whole corpus")
	}
	return parsedQuery{Required: sortedSet(req), Excluded: sortedSet(exc), Phrases: sortedSet(phr)}, nil
}

// addField folds one lexed field into the three term sets.
//
// A field that tokenizes to nothing is refused rather than skipped: `-`,
// `""` and `!!!` are each a term the caller believes they typed, and
// dropping it changes the result set without telling them. Refusing is
// also what keeps a query of nothing but punctuation from arriving at
// buildQuery as a zero-field, match-everything request.
func addField(f queryField, req, exc, phr map[string]bool) error {
	tokens := tokenize(f.text)
	if len(tokens) == 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"retrieval: query term %q holds no searchable characters", f.text)
	}
	if f.negated {
		for _, t := range tokens {
			exc[t] = true
		}
		return nil
	}
	for _, t := range tokens {
		req[t] = true
	}
	if f.isPhrase && len(tokens) > 1 {
		phr[strings.Join(tokens, " ")] = true
	}
	return nil
}

// sortedSet returns a set's members in sorted order, or nil when empty, so
// a parsedQuery is a function of the query text and not of map iteration.
func sortedSet(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
