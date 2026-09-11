package provider

import (
	"context"
	"testing"
	"time"
)

// fakeUsageReader is a minimal in-memory UsageReader implementation
// proving the interface's shape is actually usable by a third party with
// no internal/ import.
type fakeUsageReader struct {
	rows  []UsageSummary
	total int64
}

func (f fakeUsageReader) QueryUsage(_ context.Context, filter UsageFilter) ([]UsageSummary, error) {
	var out []UsageSummary
	for _, r := range f.rows {
		if filter.ProviderName != "" && r.ProviderName != filter.ProviderName {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f fakeUsageReader) TotalCostMicroUSD(_ context.Context, _ UsageFilter) (int64, error) {
	return f.total, nil
}

func TestUsageReaderInterfaceIsSatisfiable(_ *testing.T) {
	var _ UsageReader = fakeUsageReader{}
}

func TestUsageFilterZeroValueMeansNoRestriction(t *testing.T) {
	var f UsageFilter
	if f.ProviderName != "" || f.LaneName != "" || f.ModelName != "" || !f.Since.IsZero() || !f.Until.IsZero() {
		t.Fatal("zero-value UsageFilter should have every field unset")
	}
}

func TestFakeUsageReaderQueryUsageFiltersByProvider(t *testing.T) {
	reader := fakeUsageReader{
		rows: []UsageSummary{
			{ProviderName: "p1", TokensIn: 10, LastUsedAt: time.Now()},
			{ProviderName: "p2", TokensIn: 20, LastUsedAt: time.Now()},
		},
		total: 30,
	}
	got, err := reader.QueryUsage(context.Background(), UsageFilter{ProviderName: "p1"})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(got) != 1 || got[0].ProviderName != "p1" {
		t.Fatalf("unexpected filtered result: %+v", got)
	}
	total, err := reader.TotalCostMicroUSD(context.Background(), UsageFilter{})
	if err != nil {
		t.Fatalf("TotalCostMicroUSD: %v", err)
	}
	if total != 30 {
		t.Fatalf("total=%d want 30", total)
	}
}
