// Purpose: ProviderRegistryReader, the read-only subset of the providers
//   registry (internal/providers/registry, P1-E10-W3-S20-T2) that a
//   pkg/-layer consumer such as the K/S-22 conductor is allowed to see.
//   The write-capable ProviderRegistry type lives in internal/ only and is
//   never exported here, matching pkg/provider/contextiface.go's
//   MemoryReader/TokenCounter precedent: declare the seam and its data
//   shapes here so a third-party or pkg/-layer implementation never needs
//   internal/.
// Inputs: none -- interface and data-shape declarations only.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); the
//   repo-wide arch test (internal/build) asserts this boundary for every
//   file in pkg/, not only this one. This file's types are read views:
//   ProviderInfo/LaneInfo intentionally drop internal/providers/registry's
//   write-path fields (CreatedAt bookkeeping aside) a router never needs.
// SPORT: pkg.provider.registry_reader/ADD (P1-E10-W3-S20-T2).

package provider

import "context"

// ProviderInfo is the read-only view of one providers-registry provider
// record. Field names and closed-vocabulary string values mirror internal/
// providers/registry.ProviderRecord exactly, so a caller that already
// knows that package's vocabulary (driver kinds, tiers, account kinds,
// health statuses) needs no translation table.
//
//nolint:revive // contract-mandated name (04-PEWS-PLAN-W1-W3.md §Epic J S-20.T2: "ProviderRegistryReader"); the stutter with package provider is deliberate.
type ProviderInfo struct {
	Name         string
	Driver       string
	BaseURL      string
	KnownModels  []string
	AccountKind  string
	Tier         string
	Capabilities Capabilities
	HealthStatus string
}

// LaneInfo is the read-only view of one providers-registry lane record.
type LaneInfo struct {
	LaneName       string
	ProviderName   string
	ModelFilter    []string
	Weight         int
	PoolMembership string
	Capacity       string
	State          string
}

// ProviderRegistryReader is the read-only subset of the providers registry:
// exactly the five verbs a router needs (GetProvider, ListProviders,
// ListLanes, ListPool, GetByModel). The write-capable type
// (internal/providers/registry.Registry) is unreachable from here by
// construction -- this interface never names it, and pkg/ cannot import
// internal/ to name it even if it wanted to.
//
//nolint:revive // contract-mandated name (04-PEWS-PLAN-W1-W3.md §Epic J S-20.T2: "Expose a ProviderRegistryReader"); the stutter with package provider is deliberate.
type ProviderRegistryReader interface {
	// GetProvider returns the named provider, or a cascade.KindNotFound
	// error.
	GetProvider(ctx context.Context, name string) (ProviderInfo, error)
	// ListProviders returns every provider, in a stable order.
	ListProviders(ctx context.Context) ([]ProviderInfo, error)
	// ListLanes returns every lane, in a stable order.
	ListLanes(ctx context.Context) ([]LaneInfo, error)
	// ListPool returns every lane whose PoolMembership equals pool,
	// sorted by provider name.
	ListPool(ctx context.Context, pool string) ([]LaneInfo, error)
	// GetByModel returns every provider whose KnownModels contains model.
	GetByModel(ctx context.Context, model string) ([]ProviderInfo, error)
}
