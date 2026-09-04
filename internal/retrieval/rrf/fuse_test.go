package rrf

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

const tolerance = 1e-12

// hits builds a leg's candidates from bare ids, which is what most of the
// ranking assertions care about.
func hits(ids ...string) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, Candidate{ChunkID: id, Trust: corpus.TrustTrusted})
	}
	return out
}

func leg(name StrategyName, ids ...string) RankedList {
	return RankedList{Strategy: name, Weight: NeutralWeight, Hits: hits(ids...)}
}

func ids(results []FusedResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.ChunkID)
	}
	return out
}

func mustFuse(t *testing.T, lists []RankedList, k int64) []FusedResult {
	t.Helper()
	out, err := Fuse(lists, k)
	if err != nil {
		t.Fatalf("Fuse: unexpected error: %v", err)
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDefaultK_MatchesDocumentedValue asserts the constant against the
// shipped reference documentation rather than against a second copy of
// itself. A test written as DefaultK == 60 asserts nothing: it restates
// the declaration. The reference page is where a reader learns the value,
// so the two must not drift apart.
func TestDefaultK_MatchesDocumentedValue(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "reference", "retrieval.md"))
	if err != nil {
		t.Fatalf("reading the retrieval reference: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\|\s*` + "`k`" + `\s*\|\s*([0-9]+)\s*\|`)
	m := re.FindSubmatch(doc)
	if m == nil {
		t.Fatal("the retrieval reference no longer states k in a constants table row; " +
			"the documented value is what this constant is checked against")
	}
	documented, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("parsing the documented k: %v", err)
	}
	if documented != DefaultK {
		t.Errorf("DefaultK = %d, the retrieval reference documents %d", DefaultK, documented)
	}
}

// TestFuse_OrderIndependent is the property that matters most here. A
// ranker whose answer depends on the order its inputs arrived in, or on a
// map's iteration order, returns different answers for the same query on
// different runs while looking entirely correct in any single run.
func TestFuse_OrderIndependent(t *testing.T) {
	a := leg("alpha", "c1", "c2", "c3", "c4")
	b := leg("beta", "c4", "c3", "c9")
	c := leg("gamma", "c2", "c9", "c1")

	forward := mustFuse(t, []RankedList{a, b, c}, DefaultK)
	reversed := mustFuse(t, []RankedList{c, b, a}, DefaultK)
	shuffled := mustFuse(t, []RankedList{b, a, c}, DefaultK)

	if !equalIDs(ids(forward), ids(reversed)) {
		t.Errorf("leg order changed the ranking: %v then %v", ids(forward), ids(reversed))
	}
	if !equalIDs(ids(forward), ids(shuffled)) {
		t.Errorf("leg order changed the ranking: %v then %v", ids(forward), ids(shuffled))
	}
	for i := range forward {
		if forward[i].RawScore != reversed[i].RawScore {
			t.Errorf("%s: score differs by leg order: %v vs %v (bit-for-bit equality is required, "+
				"a last-bit difference is enough to flip an exact tie)",
				forward[i].ChunkID, forward[i].RawScore, reversed[i].RawScore)
		}
		if forward[i].RawScore != shuffled[i].RawScore {
			t.Errorf("%s: score differs by leg order: %v vs %v",
				forward[i].ChunkID, forward[i].RawScore, shuffled[i].RawScore)
		}
	}
}

// TestFuse_RepeatedRunsAreIdentical catches an ordering that comes from
// map iteration: Go randomizes it per run, so a dependence shows up across
// repetitions rather than within one.
func TestFuse_RepeatedRunsAreIdentical(t *testing.T) {
	lists := []RankedList{
		leg("alpha", "c1", "c2", "c3", "c4", "c5"),
		leg("beta", "c5", "c4", "c3", "c2", "c1"),
	}
	first := ids(mustFuse(t, lists, DefaultK))
	for i := 0; i < 200; i++ {
		if got := ids(mustFuse(t, lists, DefaultK)); !equalIDs(first, got) {
			t.Fatalf("run %d produced %v, first run produced %v", i, got, first)
		}
	}
}

// TestFuse_TieBreakIsAscendingChunkID pins the documented rule. Both ids
// here hold mirrored ranks, so their scores are exactly equal and only the
// tie-break decides which comes first.
func TestFuse_TieBreakIsAscendingChunkID(t *testing.T) {
	out := mustFuse(t, []RankedList{
		leg("alpha", "b", "a"),
		leg("beta", "a", "b"),
	}, DefaultK)
	if out[0].RawScore != out[1].RawScore {
		t.Fatalf("this case is only a tie-break test if the scores tie exactly: %v", out)
	}
	if !equalIDs(ids(out), []string{"a", "b"}) {
		t.Errorf("tie order = %v, want [a b]", ids(out))
	}
}

// TestFuse_TieBreakWithinEachTiedGroup checks the rule applies per group
// rather than only to the whole set. Mirrored four-element legs produce
// two tied pairs, an outer pair scoring above an inner one, and the id
// order has to hold inside each pair without disturbing the pairs.
func TestFuse_TieBreakWithinEachTiedGroup(t *testing.T) {
	out := mustFuse(t, []RankedList{
		leg("alpha", "d", "c", "b", "a"),
		leg("beta", "a", "b", "c", "d"),
	}, DefaultK)
	want := []string{"a", "d", "b", "c"}
	if !equalIDs(ids(out), want) {
		t.Fatalf("order = %v, want %v", ids(out), want)
	}
	if out[0].RawScore != out[1].RawScore || out[2].RawScore != out[3].RawScore {
		t.Errorf("the two pairs no longer tie, so this asserts nothing: %v", out)
	}
	if out[1].RawScore <= out[2].RawScore {
		t.Errorf("the outer pair must outrank the inner one: %v", out)
	}
}

// TestFuse_AbsentFromLegContributesZero checks the formula's other half:
// a chunk one leg never returned gets nothing from that leg.
func TestFuse_AbsentFromLegContributesZero(t *testing.T) {
	both := mustFuse(t, []RankedList{leg("alpha", "x"), leg("beta", "x")}, DefaultK)
	one := mustFuse(t, []RankedList{leg("alpha", "x"), leg("beta", "y")}, DefaultK)

	wantBoth := 2.0 / 61.0
	if math.Abs(both[0].RawScore-wantBoth) > tolerance {
		t.Errorf("chunk in both legs scored %v, want %v", both[0].RawScore, wantBoth)
	}
	if math.Abs(one[0].RawScore-1.0/61.0) > tolerance {
		t.Errorf("chunk in one leg scored %v, want %v", one[0].RawScore, 1.0/61.0)
	}
	if len(one[0].Strategies) != 1 || one[0].Strategies[0] != "alpha" {
		t.Errorf("strategies = %v, want only the leg that returned it", one[0].Strategies)
	}
}

// TestFuse_KChangesTheCurve proves k is used rather than hard-coded.
func TestFuse_KChangesTheCurve(t *testing.T) {
	out := mustFuse(t, []RankedList{leg("alpha", "x", "y")}, 1)
	if math.Abs(out[0].RawScore-0.5) > tolerance {
		t.Errorf("rank 1 at k=1 scored %v, want 0.5", out[0].RawScore)
	}
	if math.Abs(out[1].RawScore-1.0/3.0) > tolerance {
		t.Errorf("rank 2 at k=1 scored %v, want 1/3", out[1].RawScore)
	}
}

// TestFuse_LongAndShortLegs is the uneven-input case: one leg far longer
// than the other must not swamp or truncate the merge.
func TestFuse_LongAndShortLegs(t *testing.T) {
	long := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		long = append(long, "chunk-"+strconv.Itoa(1000+i))
	}
	out := mustFuse(t, []RankedList{
		leg("alpha", long...),
		leg("beta", "chunk-1499"),
	}, DefaultK)
	if len(out) != 500 {
		t.Fatalf("fused %d results, want 500 unique chunks", len(out))
	}
	// chunk-1499 is last in the long leg and first in the short one. Two
	// contributions beat the long leg's rank-1 hit with only one, which is
	// the whole point of fusing: agreement between legs outranks a single
	// leg's confidence.
	if out[0].ChunkID != "chunk-1499" {
		t.Errorf("top result = %s, want the chunk both legs returned", out[0].ChunkID)
	}
	if out[1].ChunkID != "chunk-1000" {
		t.Errorf("second result = %s, want the long leg's rank-1 hit", out[1].ChunkID)
	}
	if out[len(out)-1].ChunkID != "chunk-1498" {
		t.Errorf("last result = %s, want the long leg's lowest rank", out[len(out)-1].ChunkID)
	}
}

func TestFuse_ErrorPaths(t *testing.T) {
	tests := []struct {
		name  string
		lists []RankedList
		k     int64
		kind  cascade.Kind
	}{
		{"nil input", nil, DefaultK, cascade.KindInvalidInput},
		{"k zero", []RankedList{leg("alpha", "x")}, 0, cascade.KindInvalidInput},
		{"k negative", []RankedList{leg("alpha", "x")}, -1, cascade.KindInvalidInput},
		{"unnamed leg", []RankedList{{Weight: NeutralWeight, Hits: hits("x")}}, DefaultK, cascade.KindInvalidInput},
		{"repeated leg", []RankedList{leg("alpha", "x"), leg("alpha", "y")}, DefaultK, cascade.KindInvalidInput},
		{"empty chunk id", []RankedList{{Strategy: "alpha", Weight: NeutralWeight,
			Hits: []Candidate{{ChunkID: ""}}}}, DefaultK, cascade.KindInvalidInput},
		{"chunk twice in one leg", []RankedList{leg("alpha", "x", "x")}, DefaultK, cascade.KindInvalidInput},
		{"negative weight", []RankedList{{Strategy: "alpha", Weight: -1, Hits: hits("x")}},
			DefaultK, cascade.KindInvalidInput},
		{"NaN weight", []RankedList{{Strategy: "alpha", Weight: math.NaN(), Hits: hits("x")}},
			DefaultK, cascade.KindInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Fuse(tt.lists, tt.k)
			if err == nil {
				t.Fatalf("expected a refusal, got %d results", len(out))
			}
			got, ok := cascade.KindOf(err)
			if !ok || got != tt.kind {
				t.Errorf("error kind = %v (taxonomy %t), want %v", got, ok, tt.kind)
			}
			if out != nil {
				t.Errorf("a refusal must not also return results, got %v", out)
			}
		})
	}
}

// TestFuse_EmptyIsNotAnError separates "found nothing" from "was asked
// something invalid". A query that matches nothing is a normal outcome.
func TestFuse_EmptyIsNotAnError(t *testing.T) {
	for _, tt := range []struct {
		name  string
		lists []RankedList
	}{
		{"no legs at all", []RankedList{}},
		{"legs that matched nothing", []RankedList{
			{Strategy: "alpha", Weight: NeutralWeight},
			{Strategy: "beta", Weight: NeutralWeight, Hits: []Candidate{}},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Fuse(tt.lists, DefaultK)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out) != 0 {
				t.Errorf("want no results, got %v", ids(out))
			}
		})
	}
}
