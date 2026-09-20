package main

// Purpose (this file): the cost column's three states. It exists because
//   a jobs_usage row records an unpriced dispatch at 0, and 0 is where
//   "this was free" and "cascade has no prices for this provider" look
//   identical. `provider list` is where they are separated, so the
//   separation is asserted here (R-14.283).
// SPORT: cmd/cascade provider view tests (ADD) — P1-E45-W10-S88-T2.

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
)

// TestCostSummarySeparatesUnknownFromUnpriced is the assertion the whole
// column exists for.
func TestCostSummarySeparatesUnknownFromUnpriced(t *testing.T) {
	never := costSummary(nil)
	fetchedAndEmpty := costSummary(&registry.CostRecord{LastRefreshed: time.Unix(1, 0)})
	priced := costSummary(&registry.CostRecord{
		Models: map[string]registry.ModelCost{
			"a": {InputMicroUSDPerToken: 1}, "b": {InputMicroUSDPerToken: 2},
		},
	})

	if never == fetchedAndEmpty {
		t.Fatalf("a provider whose prices were NEVER fetched reads the same as one that was fetched "+
			"and has none (%q); that is the distinction this column exists to make", never)
	}
	if never != "unknown" {
		t.Errorf("no rate card reads %q, want \"unknown\"", never)
	}
	if !strings.Contains(fetchedAndEmpty, "no per-model prices") {
		t.Errorf("an empty rate card reads %q; a flat-rate subscription's normal state is not stated",
			fetchedAndEmpty)
	}
	if !strings.Contains(priced, "2") {
		t.Errorf("a two-model rate card reads %q without naming how many are priced", priced)
	}
}

// TestProviderListRowRendersEveryRegistryField keeps the row honest: the
// table line must carry the fields an operator reads a spend report
// against, not silently drop the two this ticket's sibling added.
func TestProviderListRowRendersEveryRegistryField(t *testing.T) {
	row := providerListRow{
		Name: "compatsub", Driver: "anthropic", Tier: "mid", Health: "healthy",
		AccountKind: "personal", Cost: "unknown", Lanes: 1,
	}
	line := row.String()
	for _, want := range []string{"compatsub", "anthropic", "mid", "healthy", "personal", "unknown", "lanes=1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the rendered row omits %q:\n%s", want, line)
		}
	}
}
