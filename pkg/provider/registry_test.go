// Purpose: contract test for ProviderRegistryReader -- that it is
//   satisfiable by a minimal double using only pkg/provider types, proving
//   a pkg/-layer consumer never needs internal/providers/registry to use
//   this seam.
// Constraints: the double here exists ONLY in this _test.go file (Art.1.1).
// SPORT: pkg.provider.registry_reader/ADD (P1-E10-W3-S20-T2).

package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

type fakeRegistryReader struct {
	providers map[string]provider.ProviderInfo
	pools     map[string][]provider.LaneInfo
}

func (f fakeRegistryReader) GetProvider(_ context.Context, name string) (provider.ProviderInfo, error) {
	p, ok := f.providers[name]
	if !ok {
		return provider.ProviderInfo{}, errors.New("not found")
	}
	return p, nil
}

func (f fakeRegistryReader) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	out := make([]provider.ProviderInfo, 0, len(f.providers))
	for _, p := range f.providers {
		out = append(out, p)
	}
	return out, nil
}

func (f fakeRegistryReader) ListLanes(_ context.Context) ([]provider.LaneInfo, error) {
	var out []provider.LaneInfo
	for _, lanes := range f.pools {
		out = append(out, lanes...)
	}
	return out, nil
}

func (f fakeRegistryReader) ListPool(_ context.Context, pool string) ([]provider.LaneInfo, error) {
	return f.pools[pool], nil
}

func (f fakeRegistryReader) GetByModel(_ context.Context, model string) ([]provider.ProviderInfo, error) {
	var out []provider.ProviderInfo
	for _, p := range f.providers {
		for _, m := range p.KnownModels {
			if m == model {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func TestProviderRegistryReaderGetProvider(t *testing.T) {
	var r provider.ProviderRegistryReader = fakeRegistryReader{
		providers: map[string]provider.ProviderInfo{
			"anthropic": {Name: "anthropic", Driver: "anthropic", Tier: "strong", KnownModels: []string{"claude-3-5-sonnet"}},
		},
	}
	got, err := r.GetProvider(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != "anthropic" || got.Tier != "strong" {
		t.Fatalf("GetProvider = %+v, want name=anthropic tier=strong", got)
	}
}

func TestProviderRegistryReaderGetProviderNotFound(t *testing.T) {
	var r provider.ProviderRegistryReader = fakeRegistryReader{providers: map[string]provider.ProviderInfo{}}
	if _, err := r.GetProvider(context.Background(), "missing"); err == nil {
		t.Fatal("GetProvider for a missing name should return an error")
	}
}

func TestProviderRegistryReaderGetByModel(t *testing.T) {
	var r provider.ProviderRegistryReader = fakeRegistryReader{
		providers: map[string]provider.ProviderInfo{
			"anthropic": {Name: "anthropic", KnownModels: []string{"claude-3-5-sonnet"}},
			"openai":    {Name: "openai", KnownModels: []string{"gpt-4"}},
		},
	}
	got, err := r.GetByModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("GetByModel: %v", err)
	}
	if len(got) != 1 || got[0].Name != "openai" {
		t.Fatalf("GetByModel(gpt-4) = %+v, want exactly [openai]", got)
	}
}

func TestProviderRegistryReaderListPool(t *testing.T) {
	var r provider.ProviderRegistryReader = fakeRegistryReader{
		pools: map[string][]provider.LaneInfo{
			"gf-pool": {
				{LaneName: "a", ProviderName: "a", PoolMembership: "gf-pool", State: "available"},
				{LaneName: "b", ProviderName: "b", PoolMembership: "gf-pool", State: "available"},
			},
		},
	}
	got, err := r.ListPool(context.Background(), "gf-pool")
	if err != nil {
		t.Fatalf("ListPool: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListPool = %+v, want 2 members", got)
	}
}
