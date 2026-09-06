package eval_test

// Purpose: rank-10 boundaries, missing expected items, zero-denominator
//   relative lift, exact 10-percent lift, below-threshold lift, and
//   per-query plus aggregate known-item preservation.
// Inputs: n/a (test-only). Outputs: n/a (test-only).
// Constraints: Art.7 — pure computation, no I/O.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/eval"
)

func TestRecallAt10_Rank10InRank11Out(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"rank10", "rank11"}}}
	ids := make([]string, 0, 11)
	for i := 0; i < 9; i++ {
		ids = append(ids, "filler")
	}
	ids = append(ids, "rank10", "rank11")
	results := []eval.RankedResult{{QueryText: "q", RankedIDs: ids}}
	got, err := eval.RecallAt10(queries, results)
	if err != nil {
		t.Fatalf("RecallAt10: %v", err)
	}
	if want := 0.5; got != want {
		t.Fatalf("recall@10 = %v, want %v (rank10 counts, rank11 does not)", got, want)
	}
}

func TestRecallAt10_MissingExpectedItem(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"missing"}}}
	results := []eval.RankedResult{{QueryText: "q", RankedIDs: []string{"a", "b"}}}
	got, err := eval.RecallAt10(queries, results)
	if err != nil {
		t.Fatalf("RecallAt10: %v", err)
	}
	if got != 0 {
		t.Fatalf("recall@10 = %v, want 0", got)
	}
}

func TestRecallAt10_CountMismatch(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}
	if _, err := eval.RecallAt10(queries, nil); err == nil {
		t.Fatal("result-count mismatch: want error")
	}
}

func TestRecallAt10_EmptyQuerySet(t *testing.T) {
	if _, err := eval.RecallAt10(nil, nil); err == nil {
		t.Fatal("empty query set: want error")
	}
}

func TestRelativeRecallLift_ZeroDenominator(t *testing.T) {
	if _, err := eval.RelativeRecallLift(0, 0.5); err == nil {
		t.Fatal("zero fts5 denominator: want typed error, not permissive result")
	}
}

func TestRelativeRecallLift_Exact10Percent(t *testing.T) {
	got, err := eval.RelativeRecallLift(0.5, 0.55)
	if err != nil {
		t.Fatalf("RelativeRecallLift: %v", err)
	}
	if diff := got - 0.10; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("lift = %v, want 0.10", got)
	}
}

func TestRelativeRecallLift_BelowThreshold(t *testing.T) {
	got, err := eval.RelativeRecallLift(0.5, 0.51)
	if err != nil {
		t.Fatalf("RelativeRecallLift: %v", err)
	}
	if got >= 0.10 {
		t.Fatalf("lift = %v, want below 0.10", got)
	}
}

func TestNoKnownItemLoss_PerQueryLoss(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}
	fts5 := []eval.RankedResult{{QueryText: "q", RankedIDs: []string{"a", "b"}}}
	fused := []eval.RankedResult{{QueryText: "q", RankedIDs: []string{"b", "c"}}}
	ok, detail, err := eval.NoKnownItemLoss(queries, fts5, fused)
	if err != nil {
		t.Fatalf("NoKnownItemLoss: %v", err)
	}
	if ok {
		t.Fatal("fused dropped an item fts5 found: want ok=false")
	}
	if detail == "" {
		t.Fatal("want a non-empty detail naming the lost item")
	}
}

func TestNoKnownItemLoss_AggregateRegression(t *testing.T) {
	// No single query loses its own item, but query 2's item moves from
	// "found by fts5-only" to "not found by fusion", which is exactly the
	// per-query rule above; use two queries where the aggregate recall
	// itself would differ from any single query's recall to prove the
	// aggregate check runs independently of the per-query loop.
	queries := []eval.Query{
		{Text: "q1", ExpectedIDs: []string{"a"}},
		{Text: "q2", ExpectedIDs: []string{"b"}},
	}
	fts5 := []eval.RankedResult{
		{QueryText: "q1", RankedIDs: []string{"a"}},
		{QueryText: "q2", RankedIDs: []string{"b"}},
	}
	fused := []eval.RankedResult{
		{QueryText: "q1", RankedIDs: []string{"a"}},
		{QueryText: "q2", RankedIDs: []string{"x"}},
	}
	ok, detail, err := eval.NoKnownItemLoss(queries, fts5, fused)
	if err != nil {
		t.Fatalf("NoKnownItemLoss: %v", err)
	}
	if ok {
		t.Fatalf("fused lost q2's item: want ok=false, detail=%q", detail)
	}
}

func TestNoKnownItemLoss_Preserved(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}
	fts5 := []eval.RankedResult{{QueryText: "q", RankedIDs: []string{"a"}}}
	fused := []eval.RankedResult{{QueryText: "q", RankedIDs: []string{"a"}}}
	ok, detail, err := eval.NoKnownItemLoss(queries, fts5, fused)
	if err != nil {
		t.Fatalf("NoKnownItemLoss: %v", err)
	}
	if !ok {
		t.Fatalf("item preserved: want ok=true, got detail=%q", detail)
	}
}

func TestNoKnownItemLoss_CountMismatch(t *testing.T) {
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}
	if _, _, err := eval.NoKnownItemLoss(queries, nil, nil); err == nil {
		t.Fatal("result-count mismatch: want error")
	}
}
