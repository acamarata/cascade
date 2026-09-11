package usage

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestQueryUsageFilters covers >=4 filter combinations (provider-only,
// all-nil, provider+model, date-range).
func TestQueryUsageFilters(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	seed := []IncrementRequest{
		{ProviderName: "p1", LaneName: "a", ModelName: "m1", TokensIn: 1},
		{ProviderName: "p1", LaneName: "b", ModelName: "m2", TokensIn: 2},
		{ProviderName: "p2", LaneName: "a", ModelName: "m1", TokensIn: 3},
	}
	for _, req := range seed {
		if err := mgr.IncrementUsage(ctx, req); err != nil {
			t.Fatalf("seed %+v: %v", req, err)
		}
	}

	cases := []struct {
		name   string
		filter provider.UsageFilter
		want   int
	}{
		{"provider-only", provider.UsageFilter{ProviderName: "p1"}, 2},
		{"all-nil", provider.UsageFilter{}, 3},
		{"provider+model", provider.UsageFilter{ProviderName: "p1", ModelName: "m1"}, 1},
		{"date-range-excludes-nothing", provider.UsageFilter{Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, 3},
		{"date-range-excludes-all", provider.UsageFilter{Since: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mgr.QueryUsage(ctx, tc.filter)
			if err != nil {
				t.Fatalf("QueryUsage: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d rows, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
	assertQueryGolden(ctx, t, mgr)
}

func assertQueryGolden(ctx context.Context, t *testing.T, mgr *Manager) {
	t.Helper()
	got, err := mgr.QueryUsage(ctx, provider.UsageFilter{})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	type row struct {
		Provider, Lane, Model string
		TokensIn              int64
	}
	var out []row
	for _, s := range got {
		out = append(out, row{s.ProviderName, s.LaneName, s.ModelName, s.TokensIn})
	}
	assertGolden(t, "testdata/query_filters.golden.json", out)
}

// TestTotalCostMicroUSD: aggregate SUM across filtered rows.
func TestTotalCostMicroUSD(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	reqs := []IncrementRequest{
		{ProviderName: "p1", ModelName: "m1", CostMicroUSD: 100},
		{ProviderName: "p1", ModelName: "m2", CostMicroUSD: 200},
		{ProviderName: "p2", ModelName: "m1", CostMicroUSD: 999},
	}
	for _, req := range reqs {
		if err := mgr.IncrementUsage(ctx, req); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	total, err := mgr.TotalCostMicroUSD(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("TotalCostMicroUSD: %v", err)
	}
	if total != 300 {
		t.Fatalf("total=%d want 300", total)
	}
}

// TestReaderAdapterDelegatesToManager exercises the pkg/provider.
// UsageReader adapter end to end against a real Manager.
func TestReaderAdapterDelegatesToManager(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	ctx := context.Background()
	if err := mgr.IncrementUsage(ctx, IncrementRequest{ProviderName: "p1", TokensIn: 7, CostMicroUSD: 42}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader := NewReader(mgr)
	rows, err := reader.QueryUsage(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("Reader.QueryUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].TokensIn != 7 {
		t.Fatalf("unexpected Reader.QueryUsage result: %+v", rows)
	}
	total, err := reader.TotalCostMicroUSD(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("Reader.TotalCostMicroUSD: %v", err)
	}
	if total != 42 {
		t.Fatalf("total=%d want 42", total)
	}
}

// TestTotalCostMicroUSDNoMatchingRowsReturnsZero: no matching rows returns
// (0, nil), never an error.
func TestTotalCostMicroUSDNoMatchingRowsReturnsZero(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	total, err := mgr.TotalCostMicroUSD(context.Background(), provider.UsageFilter{ProviderName: "ghost"})
	if err != nil {
		t.Fatalf("TotalCostMicroUSD: %v", err)
	}
	if total != 0 {
		t.Fatalf("total=%d want 0", total)
	}
}

// TestConcurrentIncrement: >=10 goroutines racing IncrementUsage on the
// same key; final tokens_in equals the exact sum, no data race.
func TestConcurrentIncrement(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errCh <- mgr.IncrementUsage(ctx, IncrementRequest{ProviderName: "p1", TokensIn: 1})
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	total, err := mgr.QueryUsage(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(total) != 1 || total[0].TokensIn != n {
		t.Fatalf("concurrent increment: got %+v, want tokens_in=%d", total, n)
	}
}
