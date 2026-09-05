package retrieval

// Purpose: the query parser's tests. Its whole job is to FAIL CLOSED, so
//   the cases that matter most are the malformed ones: each must refuse
//   with a typed error rather than resolve to a query that matches
//   everything, because a query that fails open returns the entire
//   authorized corpus.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: pure, in-package (the parser is unexported).
// SPORT: internal.retrieval.Index/ADDED (P1-E06-W2-S10-T2).

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFTS5QueryParserRefusesMalformed is the fail-closed table. Every row
// is a query a user could type; not one of them may parse into something
// with an empty required set, because that is the shape that matches
// every document in scope.
func TestFTS5QueryParserRefusesMalformed(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"\t\n",
		"-",
		"--",
		"!!!",
		"...   ---",
		`"`,
		`foo "bar`,
		`""`,
		`" "`,
		"-fusion",
		"-fusion -rank",
		strings.Repeat("a", maxQueryBytes+1),
		strings.TrimSpace(strings.Repeat("term ", maxQueryTerms+1)),
	} {
		got, err := parseQuery(raw)
		if err == nil {
			t.Errorf("parseQuery(%q) accepted the query as %+v; a malformed query must refuse", raw, got)
			continue
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("parseQuery(%q) returned %v, want KindInvalidInput", raw, err)
		}
	}
}

// TestFTS5QueryParserAccepts covers the language it does accept.
func TestFTS5QueryParserAccepts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want parsedQuery
	}{
		{"Reciprocal RANK", parsedQuery{Required: []string{"rank", "reciprocal"}}},
		{"fusion", parsedQuery{Required: []string{"fusion"}}},
		{"rank fusion -vector", parsedQuery{
			Required: []string{"fusion", "rank"}, Excluded: []string{"vector"}}},
		{`"rank fusion" leg`, parsedQuery{
			Required: []string{"fusion", "leg", "rank"}, Phrases: []string{"rank fusion"}}},
		{"Fusion FUSION fusion", parsedQuery{Required: []string{"fusion"}}},
		{"co-located", parsedQuery{Required: []string{"co", "located"}}},
	} {
		got, err := parseQuery(tc.raw)
		if err != nil {
			t.Errorf("parseQuery(%q): %v", tc.raw, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseQuery(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

// TestFTS5QueryParserIsDeterministic: the same text parses to the same
// value every time, including the term ORDER, which map iteration would
// otherwise scramble.
func TestFTS5QueryParserIsDeterministic(t *testing.T) {
	const raw = `zebra alpha "rank fusion" -yak -beta mike`
	first, err := parseQuery(raw)
	if err != nil {
		t.Fatalf("parseQuery: %v", err)
	}
	for i := 0; i < 32; i++ {
		again, aerr := parseQuery(raw)
		if aerr != nil {
			t.Fatalf("parseQuery: %v", aerr)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d parsed differently:\n%+v\n%+v", i, first, again)
		}
	}
	if !sortedAscending(first.Required) || !sortedAscending(first.Excluded) {
		t.Errorf("parsed terms are not sorted: %+v", first)
	}
}

func sortedAscending(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] >= s[i] {
			return false
		}
	}
	return true
}

// FuzzFTS5Query drives the parser with arbitrary input. A user's raw query
// string reaches it directly, so its properties have to hold for every
// byte sequence: never panic, never accept a query with no required term
// (which would match the whole corpus), and never exceed its own bounds.
func FuzzFTS5Query(f *testing.F) {
	for _, seed := range []string{
		"", "-", `"`, `""`, "rank fusion", `"rank fusion" -vector`,
		"\x00\x01", "�", "-\"a b\"", strings.Repeat("x ", 40),
		"co-located --x", "0123456789", "ünïcödé",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := parseQuery(raw)
		if err != nil {
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("parseQuery(%q) refused with %v, want KindInvalidInput", raw, err)
			}
			return
		}
		if len(got.Required) == 0 {
			t.Fatalf("parseQuery(%q) accepted a query with no required term; it would match everything", raw)
		}
		if len(got.Required)+len(got.Excluded) > maxQueryTerms*maxQueryTerms {
			t.Fatalf("parseQuery(%q) produced %d terms, past any bound", raw, len(got.Required))
		}
		for _, term := range append(append([]string{}, got.Required...), got.Excluded...) {
			if term == "" || len([]rune(term)) > maxTokenLen {
				t.Fatalf("parseQuery(%q) produced an out-of-bounds term %q", raw, term)
			}
		}
	})
}
