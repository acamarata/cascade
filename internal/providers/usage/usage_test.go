package usage

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestIncrementUsageAccumulation: three sequential calls on the same key
// produce tokens_in = sum, requests = 3, errors = count of Error=true
// calls; a golden asserts the stored record after each call.
func TestIncrementUsageAccumulation(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC))
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	calls := []IncrementRequest{
		{ProviderName: "anthropic", LaneName: "main", ModelName: "claude-sonnet", TokensIn: 100, TokensOut: 20, CostMicroUSD: 500},
		{ProviderName: "anthropic", LaneName: "main", ModelName: "claude-sonnet", TokensIn: 50, TokensOut: 10, CostMicroUSD: 250, Error: true},
		{ProviderName: "anthropic", LaneName: "main", ModelName: "claude-sonnet", TokensIn: 25, TokensOut: 5, CostMicroUSD: 125},
	}
	var snapshots []map[string]any
	for i, req := range calls {
		if err := mgr.IncrementUsage(ctx, req); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		got, err := mgr.QueryUsage(ctx, provider.UsageFilter{ProviderName: "anthropic"})
		if err != nil {
			t.Fatalf("QueryUsage after call %d: %v", i, err)
		}
		if len(got) != 1 {
			t.Fatalf("call %d: expected 1 row, got %d", i, len(got))
		}
		s := got[0]
		snapshots = append(snapshots, map[string]any{
			"tokens_in": s.TokensIn, "tokens_out": s.TokensOut, "requests": s.Requests,
			"errors": s.Errors, "cost_micro_usd": s.CostMicroUSD,
		})
	}
	assertGolden(t, "testdata/accumulation.golden.json", snapshots)

	final := snapshots[2]
	if final["tokens_in"] != int64(175) || final["requests"] != int64(3) || final["errors"] != int64(1) {
		t.Fatalf("unexpected final accumulation: %+v", final)
	}
}

// TestBucketIsolation: calls for the same (provider, lane, model) on two
// different bucket dates produce two distinct rows; a Since/Until filter
// returns only the matching day.
func TestBucketIsolation(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	mgr := newTestManager(t, clk)
	ctx := context.Background()
	req := IncrementRequest{ProviderName: "p1", TokensIn: 10}

	if err := mgr.IncrementUsage(ctx, req); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	clk.Set(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if err := mgr.IncrementUsage(ctx, req); err != nil {
		t.Fatalf("day 2: %v", err)
	}

	all, err := mgr.QueryUsage(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("QueryUsage all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 distinct bucket rows, got %d", len(all))
	}

	day1Only, err := mgr.QueryUsage(ctx, provider.UsageFilter{
		ProviderName: "p1",
		Since:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until:        time.Date(2026, 9, 1, 23, 59, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("QueryUsage day1: %v", err)
	}
	if len(day1Only) != 1 || day1Only[0].Bucket != "2026-09-01" {
		t.Fatalf("date-range filter did not isolate day 1: %+v", day1Only)
	}
}

// TestNilCostPassthrough: CostMicroUSD=0 stores 0 without error; the
// total-cost query returns 0.
func TestNilCostPassthrough(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	if err := mgr.IncrementUsage(ctx, IncrementRequest{ProviderName: "p1", TokensIn: 5, CostMicroUSD: 0}); err != nil {
		t.Fatalf("IncrementUsage: %v", err)
	}
	total, err := mgr.TotalCostMicroUSD(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("TotalCostMicroUSD: %v", err)
	}
	if total != 0 {
		t.Fatalf("total=%d want 0", total)
	}
}

// TestIncrementUsageRejectsNegativeCounters proves the fail-closed
// overflow/negative guard.
func TestIncrementUsageRejectsNegativeCounters(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	if err := mgr.IncrementUsage(context.Background(), IncrementRequest{ProviderName: "p1", TokensIn: -1}); err == nil {
		t.Fatal("expected an error for a negative TokensIn")
	}
}

// TestIncrementUsageOverflowRefusesAndLeavesRowUnchanged proves
// IncrementUsage's own overflow guard (not just checkedAdd in isolation):
// a second call that would overflow tokens_in is refused and the stored
// row keeps its pre-overflow value.
func TestIncrementUsageOverflowRefusesAndLeavesRowUnchanged(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	ctx := context.Background()

	if err := mgr.IncrementUsage(ctx, IncrementRequest{ProviderName: "p1", TokensIn: math.MaxInt64}); err != nil {
		t.Fatalf("seed max value: %v", err)
	}
	if err := mgr.IncrementUsage(ctx, IncrementRequest{ProviderName: "p1", TokensIn: 1}); err == nil {
		t.Fatal("expected an overflow error")
	}
	rows, err := mgr.QueryUsage(ctx, provider.UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].TokensIn != math.MaxInt64 {
		t.Fatalf("row mutated despite overflow refusal: %+v", rows)
	}
}

func TestIncrementUsageRequiresProviderName(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	mgr := newTestManager(t, clk)
	if err := mgr.IncrementUsage(context.Background(), IncrementRequest{TokensIn: 1}); err == nil {
		t.Fatal("expected an error for an empty provider_name")
	}
}

func TestCheckedAddOverflowRefuses(t *testing.T) {
	if _, err := checkedAdd(9223372036854775807, 1); err == nil {
		t.Fatal("expected an overflow error")
	}
	if got, err := checkedAdd(5, 5); err != nil || got != 10 {
		t.Fatalf("checkedAdd(5,5) = (%d, %v), want (10, nil)", got, err)
	}
}

// assertGolden compares got (JSON-marshaled) against the fixture at path.
func assertGolden(t *testing.T, path string, got any) {
	t.Helper()
	gotBytes, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	if os.Getenv("CASCADE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, append(gotBytes, '\n'), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	var wantVal, gotVal any
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("decode golden %s: %v", path, err)
	}
	if err := json.Unmarshal(gotBytes, &gotVal); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	wantJSON, _ := json.Marshal(wantVal)
	gotJSON, _ := json.Marshal(gotVal)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("golden mismatch for %s:\n want=%s\n got=%s", path, wantJSON, gotJSON)
	}
}
