// Package vectorizer is the committed, deterministic term-count vectorizer
// behind internal/retrieval/eval/testdata/recorded/real-embedder.jsonl.
//
// It is NOT a trained embedding model. It is a lexical bag of words over a
// fixed vocabulary, kept honest by saying exactly what it computes.
//
// # Method specification
//
// Vocabulary: a text file with one term per line. Blank lines and lines whose
// first character is "#" are ignored. The order of the remaining lines is the
// dimension order: the first term is dimension 0. A term is a non-empty,
// lower-case run of letters, digits and underscores; a term that no token
// could ever equal is refused. Two lines with the same term are refused, and a
// vocabulary with no terms is refused (KindInvalidInput for all of them).
//
// Tokenizer (applied to the text, never to the vocabulary):
//  1. lower-case the whole text with Unicode simple case mapping
//     (strings.ToLower);
//  2. a token is a maximal run of runes that are Unicode letters, Unicode
//     decimal digits or the underscore; every other rune separates tokens.
//
// The underscore belongs to a token so that identifiers such as snake_case or
// an environment variable name stay one token. That is the rule the original
// 2026-09-06 recording followed, which is why unchanged texts reproduce its
// vectors exactly.
//
// Vectorize: dimension d of the vector is the number of tokens equal to term d
// plus a smoothing constant of 0.01 (so no vector is all zero and a cosine
// score is always defined). The vector has one component per term, in
// vocabulary order, as float32.
//
// Record: writes the JSON document eval.LoadRecordedEmbeddings reads, with a
// fixed field order (tool, version, date, records), two-space indentation, one
// vector component per line, ASCII-only string escapes, records in order of
// first appearance of their text (a repeated text is recorded once) and a
// trailing newline. Nothing in the output depends on the clock, the process
// or map iteration order.
//
// Inputs: a vocabulary reader, texts, a Provenance. Outputs: vectors and the
// recording bytes. Constraints: standard library plus pkg/cascade only; no
// clock, no randomness, no filesystem, no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-GWY-12).
package vectorizer

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // vocabulary.txt is compiled in so the generator and its tests share one source.
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
)

// smoothing is added to every component (see the method specification).
const smoothing = 0.01

//go:embed vocabulary.txt
var defaultVocabulary []byte

// Provenance names what produced a recording: the generator and vocabulary
// (Tool), a version string and the regeneration date (YYYY-MM-DD).
type Provenance struct {
	Tool    string
	Version string
	Date    string
}

// Vocabulary is an ordered, duplicate-free term list.
type Vocabulary struct {
	terms  []string
	index  map[string]int
	digest string
}

// Default loads the vocabulary committed beside this package.
func Default() (Vocabulary, error) {
	return Load(bytes.NewReader(defaultVocabulary))
}

// Load parses a vocabulary per the method specification. The digest is
// computed over the exact bytes read.
func Load(vocab io.Reader) (Vocabulary, error) {
	raw, err := io.ReadAll(vocab)
	if err != nil {
		return Vocabulary{}, cascade.Wrap(cascade.KindInvalidInput, err, "vectorizer: reading vocabulary")
	}
	v := Vocabulary{index: map[string]int{}}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !validTerm(line) {
			return Vocabulary{}, cascade.Newf(cascade.KindInvalidInput,
				"vectorizer: vocabulary line %d: term %q is not a lower-case run of letters, digits and underscores", i+1, line)
		}
		if dim, dup := v.index[line]; dup {
			return Vocabulary{}, cascade.Newf(cascade.KindInvalidInput,
				"vectorizer: vocabulary line %d repeats term %q of dimension %d", i+1, line, dim)
		}
		v.index[line] = len(v.terms)
		v.terms = append(v.terms, line)
	}
	if len(v.terms) == 0 {
		return Vocabulary{}, cascade.New(cascade.KindInvalidInput, "vectorizer: vocabulary has no terms")
	}
	sum := sha256.Sum256(raw)
	v.digest = hex.EncodeToString(sum[:])
	return v, nil
}

// Digest is the SHA-256 (hex) of the vocabulary bytes Load read.
func (v Vocabulary) Digest() string { return v.digest }

// isTokenRune reports whether r belongs inside a token.
func isTokenRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// validTerm reports whether term is a non-empty lower-case token.
func validTerm(term string) bool {
	if term == "" || term != strings.ToLower(term) {
		return false
	}
	for _, r := range term {
		if !isTokenRune(r) {
			return false
		}
	}
	return true
}

// Vectorize returns text's vector: one component per vocabulary term, each the
// token count of that term plus the smoothing constant.
func (v Vocabulary) Vectorize(text string) []float32 {
	counts := make([]int, len(v.terms))
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !isTokenRune(r) }) {
		if dim, ok := v.index[tok]; ok {
			counts[dim]++
		}
	}
	out := make([]float32, len(counts))
	for i, n := range counts {
		out[i] = float32(float64(n) + smoothing)
	}
	return out
}

// Record renders the recording document for texts, in order of first
// appearance, each vectorized with v.
func Record(v Vocabulary, prov Provenance, texts []string) ([]byte, error) {
	if prov.Tool == "" || prov.Version == "" || prov.Date == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "vectorizer: provenance needs tool, version and date")
	}
	if len(texts) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "vectorizer: no texts to record")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"tool\": %s,\n  \"version\": %s,\n  \"date\": %s,\n  \"records\": [\n",
		jsonString(prov.Tool), jsonString(prov.Version), jsonString(prov.Date))
	seen := make(map[string]bool, len(texts))
	first := true
	for i, text := range texts {
		if text == "" {
			return nil, cascade.Newf(cascade.KindInvalidInput, "vectorizer: text %d is empty", i)
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		if !first {
			b.WriteString(",\n")
		}
		first = false
		writeRecord(&b, text, v.Vectorize(text))
	}
	b.WriteString("\n  ]\n}\n")
	return b.Bytes(), nil
}

// writeRecord appends one {text, vector} object at record indentation.
func writeRecord(b *bytes.Buffer, text string, vec []float32) {
	fmt.Fprintf(b, "    {\n      \"text\": %s,\n      \"vector\": [\n", jsonString(text))
	for i, f := range vec {
		b.WriteString("        " + strconv.FormatFloat(float64(f), 'f', -1, 32))
		if i < len(vec)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("      ]\n    }")
}

// jsonString quotes s as a JSON string using ASCII only: the two-character
// escapes for quote, backslash and the usual control characters, \u00XX for
// the others, and \uXXXX (surrogate pairs above U+FFFF) for every rune
// outside space..tilde. Printable ASCII is written as is, including < > and &.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r >= ' ' && r <= '~':
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
