// Purpose: the runnable godoc Example for provider.Reranker (Art.10.6) and
//   the contract tests for the Rerank result shape, driven through a double
//   that holds the contract and doubles that break each clause of it.
// Constraints: every double here exists ONLY in this _test.go file (Art.1.1);
//   no implementation ships from this ticket.
// SPORT: pkg.provider.Reranker/ADDED (P1-E06-W2-S10-T5).

package provider_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// testReranker is a contract-holding Reranker: it scores a passage by the
// share of the query's words it contains, returns every input passage
// exactly once, and orders the result best first. The scoring is a
// demonstration, not a relevance model.
type testReranker struct{}

var _ provider.Reranker = (*testReranker)(nil)

func (r *testReranker) Rerank(ctx context.Context, query string, passages []string) ([]provider.RankedPassage, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "rerank aborted")
	}
	terms := strings.Fields(strings.ToLower(query))
	ranked := make([]provider.RankedPassage, 0, len(passages))
	for _, p := range passages {
		ranked = append(ranked, provider.RankedPassage{Text: p, Score: overlap(terms, p)})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	return ranked, nil
}

func overlap(terms []string, passage string) float64 {
	if len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(passage)
	var hits float64
	for _, t := range terms {
		if strings.Contains(lower, t) {
			hits++
		}
	}
	return hits / float64(len(terms))
}

// ExampleReranker reorders first-pass candidates against a query and checks
// the result against the Rerank contract before trusting the order.
func ExampleReranker() {
	ctx := context.Background()
	var rr provider.Reranker = &testReranker{}

	passages := []string{
		"the qibla direction is computed from great-circle bearing",
		"a changelog entry documents a release",
		"great-circle distance on a sphere",
	}
	ranked, err := rr.Rerank(ctx, "great circle", passages)
	if err != nil {
		fmt.Println("rerank error:", err)
		return
	}

	fmt.Println(provider.ValidRanking(passages, ranked))
	fmt.Println(ranked[0].Score, ranked[len(ranked)-1].Score)
	fmt.Println(ranked[len(ranked)-1].Text)

	// Output:
	// true
	// 1 0
	// a changelog entry documents a release
}

func TestRerank_EmptyPassagesIsNotAnError(t *testing.T) {
	rr := &testReranker{}
	for name, passages := range map[string][]string{
		"nil":   nil,
		"empty": {},
	} {
		ranked, err := rr.Rerank(context.Background(), "any query", passages)
		if err != nil {
			t.Fatalf("%s candidates: unexpected error: %v", name, err)
		}
		if len(ranked) != 0 {
			t.Fatalf("%s candidates: got %d results, want 0", name, len(ranked))
		}
	}
}

func TestRerank_CanceledContextReturnsTaxonomyError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ranked, err := (&testReranker{}).Rerank(ctx, "q", []string{"p"})
	if err == nil {
		t.Fatal("want an error from a canceled context, got nil")
	}
	if ranked != nil {
		t.Fatalf("all-or-nothing: want a nil slice alongside the error, got %+v", ranked)
	}
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("want a KindCanceled taxonomy error, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the context cause preserved, got %v", err)
	}
}

func TestRerank_PreservesDuplicatesAndTextVerbatim(t *testing.T) {
	passages := []string{"alpha beta", "alpha beta", "gamma"}
	ranked, err := (&testReranker{}).Rerank(context.Background(), "alpha", passages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !provider.ValidRanking(passages, ranked) {
		t.Fatalf("a duplicate candidate must come back twice: %+v", ranked)
	}
}

// TestValidRanking runs the structural half of the Rerank contract against
// results that break each clause of it.
func TestValidRanking(t *testing.T) {
	passages := []string{"a", "b", "c"}
	ok := []provider.RankedPassage{{Text: "c", Score: 3}, {Text: "a", Score: 2}, {Text: "b", Score: 1}}

	cases := map[string]struct {
		ranked []provider.RankedPassage
		want   bool
	}{
		"conforming":       {ok, true},
		"tied scores":      {[]provider.RankedPassage{{Text: "a"}, {Text: "b"}, {Text: "c"}}, true},
		"dropped passage":  {ok[:2], false},
		"duplicated entry": {[]provider.RankedPassage{ok[0], ok[0], ok[1]}, false},
		"invented passage": {[]provider.RankedPassage{ok[0], ok[1], {Text: "d", Score: 1}}, false},
		"edited text":      {[]provider.RankedPassage{ok[0], ok[1], {Text: "B", Score: 1}}, false},
		"ascending order":  {[]provider.RankedPassage{ok[2], ok[1], ok[0]}, false},
	}
	for name, tc := range cases {
		if got := provider.ValidRanking(passages, tc.ranked); got != tc.want {
			t.Fatalf("%s: ValidRanking = %v, want %v", name, got, tc.want)
		}
	}
}

func TestValidRanking_EmptyIsValid(t *testing.T) {
	if !provider.ValidRanking(nil, nil) {
		t.Fatal("an empty candidate set ranks to an empty result")
	}
}

// TestValidRanking_CatchesAViolatingImplementation drives a double that
// silently truncates to a top-1 through the same check a caller would use.
func TestValidRanking_CatchesAViolatingImplementation(t *testing.T) {
	passages := []string{"alpha", "beta", "gamma"}
	ranked, err := (&truncatingReranker{}).Rerank(context.Background(), "alpha", passages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.ValidRanking(passages, ranked) {
		t.Fatal("a reranker that drops candidates must not pass the ranking check")
	}
}

// truncatingReranker violates completeness by returning only its best
// candidate, the failure mode that silently loses retrieval recall.
type truncatingReranker struct{}

var _ provider.Reranker = (*truncatingReranker)(nil)

func (r *truncatingReranker) Rerank(ctx context.Context, query string, passages []string) ([]provider.RankedPassage, error) {
	ranked, err := (&testReranker{}).Rerank(ctx, query, passages)
	if err != nil || len(ranked) == 0 {
		return ranked, err
	}
	return ranked[:1], nil
}
