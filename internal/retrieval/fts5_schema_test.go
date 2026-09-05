package retrieval

// Purpose: the index schema's tests — the tokenizer both the write path
//   and the query parser share, the key layout, and the codecs' refusal
//   to hand back a row they could not read whole.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: pure, in-package (the schema is unexported).
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFTS5SchemaTokenize pins the analysis. It is asserted against stated
// expectations, not against a second call of the same function.
func TestFTS5SchemaTokenize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   \n\t", nil},
		{"Rank Fusion", []string{"rank", "fusion"}},
		{"rank, fusion; rank", []string{"rank", "fusion", "rank"}},
		{"co-located", []string{"co", "located"}},
		{"UTF-8 café", []string{"utf", "8", "caf"}},
		{"x" + strings.Repeat("y", maxTokenLen*2), []string{"x" + strings.Repeat("y", maxTokenLen-1)}},
	} {
		if got := tokenize(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestFTS5SchemaTokenCounts checks the fold the write path stores: sorted
// distinct tokens, their counts in the same order, and the total length.
func TestFTS5SchemaTokenCounts(t *testing.T) {
	tokens, counts, length := tokenCounts("rank fusion rank RANK merge")
	if want := []string{"fusion", "merge", "rank"}; !reflect.DeepEqual(tokens, want) {
		t.Errorf("tokens = %v, want %v", tokens, want)
	}
	if want := []int{1, 1, 3}; !reflect.DeepEqual(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	if length != 5 {
		t.Errorf("length = %d, want 5 (occurrences, not distinct tokens)", length)
	}
	if _, _, n := tokenCounts(""); n != 0 {
		t.Errorf("empty text has length %d, want 0", n)
	}
}

// TestFTS5SchemaKeys pins the key layout the store rows and the scan
// prefixes agree on.
func TestFTS5SchemaKeys(t *testing.T) {
	if got, want := docKey("abc"), "fts:doc:abc"; got != want {
		t.Errorf("docKey = %q, want %q", got, want)
	}
	if got, want := tokenKey("rank", "abc"), "fts:tok:rank:abc"; got != want {
		t.Errorf("tokenKey = %q, want %q", got, want)
	}
	if !strings.HasPrefix(tokenKey("rank", "abc"), tokenScanPrefix("rank")) {
		t.Error("a posting key does not start with its own scan prefix")
	}
	if got, want := statKey("handbook"), "fts:stat:handbook"; got != want {
		t.Errorf("statKey = %q, want %q", got, want)
	}
	if IndexNamespace != "retrieval" {
		t.Errorf("IndexNamespace = %q, want the ratified retrieval domain", IndexNamespace)
	}
}

// TestFTS5SchemaCodecsRefuseUnreadable: a row that cannot be read whole is
// an integrity refusal, never a silently skipped row, because a query that
// dropped rows it could not read would return a short answer that looks
// complete.
func TestFTS5SchemaCodecsRefuseUnreadable(t *testing.T) {
	if _, err := decodeDocument([]byte("{not json")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("decodeDocument on garbage returned %v, want KindIntegrity", err)
	}
	mismatched := []byte(`{"chunk_id":"a","tokens":["x","y"],"frequencies":[1]}`)
	if _, err := decodeDocument(mismatched); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("decodeDocument on a token/frequency mismatch returned %v, want KindIntegrity", err)
	}
	if _, err := decodeStats([]byte("nope")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("decodeStats on garbage returned %v, want KindIntegrity", err)
	}
	for _, bad := range [][]byte{[]byte(""), []byte("0"), []byte("-3"), []byte("x")} {
		if _, err := decodeFrequency(bad); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("decodeFrequency(%q) returned %v, want KindIntegrity", bad, err)
		}
	}
	if n, err := decodeFrequency(encodeFrequency(7)); err != nil || n != 7 {
		t.Errorf("frequency round-trip gave (%d, %v), want (7, nil)", n, err)
	}
}

// TestFTS5SchemaDocumentRoundTrip: a row encodes and decodes to itself,
// and encoding is byte-stable across calls (Art.7).
func TestFTS5SchemaDocumentRoundTrip(t *testing.T) {
	doc := document{
		ChunkID: "abc", Path: "a.md", CorpusID: "handbook", Lang: "markdown",
		Content: "rank fusion", Tokens: []string{"fusion", "rank"},
		Frequencies: []int{1, 1}, Length: 2,
	}
	first, err := encodeDocument(doc)
	if err != nil {
		t.Fatalf("encodeDocument: %v", err)
	}
	second, err := encodeDocument(doc)
	if err != nil {
		t.Fatalf("encodeDocument: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("two encodings differ:\n%s\n%s", first, second)
	}
	got, err := decodeDocument(first)
	if err != nil {
		t.Fatalf("decodeDocument: %v", err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("round trip gave %+v, want %+v", got, doc)
	}
}

// TestFTS5ScoreEdges pins the scoring guards: no division by zero on a
// statistics row that has not caught up, and no negative weight from a
// document frequency past the document count.
func TestFTS5ScoreEdges(t *testing.T) {
	if got := idf(0, 10); got != 0 {
		t.Errorf("idf with no document frequency = %v, want 0", got)
	}
	if got := idf(5, 0); got != 0 {
		t.Errorf("idf with no documents = %v, want 0", got)
	}
	if got := idf(50, 10); got < 0 {
		t.Errorf("idf(df > n) = %v, want a non-negative weight", got)
	}
	if got := avgLength(corpusStats{}, document{Length: 4}); got != 4 {
		t.Errorf("avgLength fell back to %v, want the document's own length", got)
	}
	if got := avgLength(corpusStats{}, document{}); got != 1 {
		t.Errorf("avgLength with nothing to go on = %v, want 1", got)
	}
	if got := snippet("", []string{"x"}); got != "" {
		t.Errorf("snippet of empty content = %q", got)
	}
	long := strings.Repeat("padding ", 100) + "needle tail"
	if got := snippet(long, []string{"needle"}); !strings.Contains(got, "needle") {
		t.Errorf("snippet did not reach the match: %q", got)
	}
}
