//go:build !windows

package main

// Purpose (this file): behaviour coverage for the accounting paths wired
//   at the composition root — what a dispatch is priced at, and what a
//   zero price MEANS.
//
// WHY IT MATTERS MORE THAN THE PERCENTAGE. The registry's Cost is nil for
//   "prices were never fetched", so an unpriced provider's dispatch is
//   recorded at 0. That 0 is not "free", and the only thing standing
//   between those two readings is this estimator returning it for the
//   right reason (R-14.283). A test that only asserted "0" would pass on
//   an estimator that always returned 0.
//
// SPORT: cmd/cascade accounting tests (ADD) — P1-E45-W10-S88-T2.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
)

// TestRateCardEstimatorPricesFromTheRegistry is the success path: a
// provider WITH a rate card is priced from it, per token, both directions.
func TestRateCardEstimatorPricesFromTheRegistry(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)
	rec := registry.ProviderRecord{
		Name: "priced", Driver: "anthropic", Auth: "key", AuthRef: "provider.priced.key",
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid,
		Cost: &registry.CostRecord{Models: map[string]registry.ModelCost{
			"m-1": {InputMicroUSDPerToken: 3, OutputMicroUSDPerToken: 15},
		}},
	}
	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	est := rateCardEstimator{reg: reg}

	// 1000 in at 3, 200 out at 15 => 3000 + 3000.
	if got := est.EstimateCostMicroUSD("priced", "m-1", 1000, 200); got != 6000 {
		t.Errorf("EstimateCostMicroUSD = %d, want 6000 (1000*3 + 200*15)", got)
	}
	// Asymmetry is the point: output tokens are the expensive half, and
	// an estimator that summed one rate over both would pass a symmetric
	// fixture.
	if in, out := est.EstimateCostMicroUSD("priced", "m-1", 100, 0),
		est.EstimateCostMicroUSD("priced", "m-1", 0, 100); in == out {
		t.Errorf("input and output price the same (%d); the rate card's two rates are not both read", in)
	}
}

// TestRateCardEstimatorReturnsZeroOnlyWhenItKnowsNothing walks the three
// ways a price is unknown. Each must be 0 — and each is a DIFFERENT fact
// from "this was free", which is why `provider list` carries a cost
// column and a jobs_usage row does not.
func TestRateCardEstimatorReturnsZeroOnlyWhenItKnowsNothing(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)
	unpriced := registry.ProviderRecord{
		Name: "unpriced", Driver: "anthropic", Auth: "key", AuthRef: "provider.unpriced.key",
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid,
	}
	if err := reg.AddProvider(ctx, unpriced); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	for _, tc := range []struct {
		name     string
		est      rateCardEstimator
		provider string
		model    string
	}{
		{"no registry at all", rateCardEstimator{}, "priced", "m-1"},
		{"provider not registered", rateCardEstimator{reg: reg}, "absent", "m-1"},
		{"registered with no rate card", rateCardEstimator{reg: reg}, "unpriced", "m-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.est.EstimateCostMicroUSD(tc.provider, tc.model, 1000, 1000); got != 0 {
				t.Errorf("EstimateCostMicroUSD = %d, want 0 (price unknown, not free)", got)
			}
		})
	}
}

// TestRateCardEstimatorRefusesToGuessAnUnpricedModel: a provider may have
// prices for some models and not others. The unpriced one is 0, and the
// priced one in the SAME registry still prices — so the zero is about the
// model, not about a lookup that quietly failed.
func TestRateCardEstimatorRefusesToGuessAnUnpricedModel(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)
	rec := registry.ProviderRecord{
		Name: "partial", Driver: "anthropic", Auth: "key", AuthRef: "provider.partial.key",
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid,
		Cost: &registry.CostRecord{Models: map[string]registry.ModelCost{
			"known": {InputMicroUSDPerToken: 2, OutputMicroUSDPerToken: 2},
		}},
	}
	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	est := rateCardEstimator{reg: reg}

	if got := est.EstimateCostMicroUSD("partial", "unknown-model", 10, 10); got != 0 {
		t.Errorf("an unpriced model cost %d; the estimator invented a price", got)
	}
	if got := est.EstimateCostMicroUSD("partial", "known", 10, 10); got != 40 {
		t.Errorf("the priced model in the same registry cost %d, want 40; the lookup is broken, "+
			"not the rate card", got)
	}
}
