// Purpose: UsageReader, the read-only per-lane provider usage accounting
//   surface (P1-E10-W3-S20-T4) a pkg/-layer consumer (the provider CLI's
//   `cascade provider usage` display, S-21.T2) uses to query recorded
//   token/cost counters. The write-capable UsageManager lives in
//   internal/providers/usage/ only, matching pkg/provider/registry.go's
//   ProviderRegistryReader precedent.
// Inputs: none -- interface and data-shape declarations only.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); the
//   repo-wide arch test asserts this boundary for every file in pkg/.
// SPORT: pkg.provider.usage_reader/ADD (P1-E10-W3-S20-T4).

package provider

import (
	"context"
	"time"
)

// UsageFilter selects which provider_usage rows QueryUsage/
// TotalCostMicroUSD aggregate over. Every field is optional; the zero
// value ("" / zero time.Time) means "no restriction on this dimension".
type UsageFilter struct {
	ProviderName string
	LaneName     string
	ModelName    string
	Since        time.Time
	Until        time.Time
}

// UsageSummary is one aggregated provider_usage row: the sums for one
// (provider_name, lane_name, model_name, bucket) key matched by a
// UsageFilter.
type UsageSummary struct {
	ProviderName string
	LaneName     string
	ModelName    string
	Bucket       string // YYYY-MM-DD day bucket
	TokensIn     int64
	TokensOut    int64
	Requests     int64
	Errors       int64
	CostMicroUSD int64
	LastUsedAt   time.Time
}

// UsageReader is the read-only subset of the usage accounting domain: the
// two verbs a router or CLI consumer needs. The write-capable type
// (internal/providers/usage.Manager) is unreachable from here by
// construction -- this interface never names it, and pkg/ cannot import
// internal/ to name it even if it wanted to.
type UsageReader interface {
	// QueryUsage returns every UsageSummary matching filter, ordered by
	// provider_name, then lane_name, then model_name, then bucket
	// descending.
	QueryUsage(ctx context.Context, filter UsageFilter) ([]UsageSummary, error)
	// TotalCostMicroUSD sums cost_micro_usd across every row matching
	// filter.
	TotalCostMicroUSD(ctx context.Context, filter UsageFilter) (int64, error)
}
