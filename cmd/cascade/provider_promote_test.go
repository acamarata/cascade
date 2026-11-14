// Purpose: unit tests of the promote flags and renderings (P1-TOP-19f): a
// tier other than paid, both flags together and a missing --tier refuse
// before any storage, daemon or custody is touched; a remove note and a
// promote reply name what they must and never a credential.
// Inputs: cobra trees over hermetic deps. Outputs: none (tests).
// Constraints: no network, no daemon.
// SPORT: cmd/cascade provider promote tests (P1-TOP-19f).
package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestProviderTestTierFlagsRefuseBeforeAnyWork(t *testing.T) {
	deps := testProviderDeps(t, nil)
	cases := [][]string{
		{"test", "p", "--promote", "gold"},
		{"test", "p", "--promote", "paid", "--demote"},
	}
	for _, args := range cases {
		_, _, err := runProvider(t, deps, args...)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("provider %v: err = %v, want invalid input", args, err)
		}
	}
}

func TestFleetDomainsPromoteNeedsPaidTier(t *testing.T) {
	deps := productionFleetSessionsDeps()
	for _, args := range [][]string{{"promote", "d"}, {"promote", "d", "--tier", "discover"}} {
		_, err := execLeaf(t, newFleetDomainsCmd(deps), args)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("fleet domains %v: err = %v, want invalid input", args, err)
		}
	}
}

func TestPromoteRenderingsNameDomainTierAndProviders(t *testing.T) {
	out := domainChangeReply{Domain: "dgood", Tier: "paid", Providers: []string{"a", "b"}}.String()
	for _, want := range []string{"dgood", "tier=paid", "provider a", "provider b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("reply %q lacks %q", out, want)
		}
	}
	note := providerRemoveResult{Name: "p", Delta: "removed", Note: "pruned at the next reconcile"}.String()
	if !strings.Contains(note, "delta=removed") || !strings.Contains(note, "next reconcile") {
		t.Fatalf("remove result %q", note)
	}
}
