package daemon

// Purpose: the daemon composition wires topology.SensitivityTable to the
// conductor's lane table, so the lease floor admits a public lease and refuses a
// restricted one on an external lane, and an unresolved lane never admits.
// SPORT: internal/daemon (ADD, P1-TOP-02dw).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

var allTiers = []provider.SensitivityTier{
	provider.SensitivityLocalOnly, provider.SensitivityRestricted,
	provider.SensitivityInternal, provider.SensitivityPublic,
}

// seedFreeLane seeds one free-billing credential and lane named name into s.
func seedFreeLane(t *testing.T, s *topology.Store, name string) {
	t.Helper()
	ctx := context.Background()
	acct, domain := topology.AccountID("acct-"+name), topology.DomainID(name+":api")
	profile := topology.RuntimeProfile{ID: topology.RuntimeProfileID("prof-" + name), Runtime: topology.RuntimeAPI, Persistent: true}
	cred := topology.CredentialID(name)
	steps := []func() error{
		func() error {
			return s.UpsertAccount(ctx, topology.Account{ID: acct, Provider: "openai_compat", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce})
		},
		func() error {
			return s.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: domain, AccountRef: acct, Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierFree})
		},
		func() error { return s.UpsertRuntimeProfile(ctx, profile) },
		func() error {
			return s.UpsertCredential(ctx, topology.Credential{ID: cred, AccountRef: acct, QuotaDomainRef: domain,
				SecretRef: topology.VaultKeyRef("provider." + name + ".key"), RuntimeProfileRef: profile.ID, Health: topology.CredentialOK})
		},
		func() error {
			id := topology.LaneIDFor(profile.ID, domain, "", "", topology.EffortNA, topology.InteractionInteractive)
			return s.UpsertLane(ctx, topology.Lane{ID: id, RuntimeProfileRef: profile.ID, QuotaDomainRef: domain, CredentialRef: &cred,
				Effort: topology.EffortNA, InteractionClass: topology.InteractionInteractive, Health: topology.LaneHealthAvailable,
				LaneClass: topology.LaneClassUnranked, BaseShadowPrice: 1})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
}

func TestDaemonWiresTopologySensitivityTable(t *testing.T) {
	saved := topology.SensitivityTable
	topology.SensitivityTable = nil
	t.Cleanup(func() { topology.SensitivityTable = saved })

	_, r, _ := newCapRig(t)
	table := topology.SensitivityTable
	if table == nil {
		t.Fatal("topology.SensitivityTable is nil after the daemon composition")
	}
	kinds := map[topology.LaneKind]conductor.LaneType{
		topology.LaneKindControllerLocal: conductor.LaneControllerLocal,
		topology.LaneKindExternalAPI:     conductor.LaneExternalAPI,
	}
	for k, lt := range kinds {
		for _, tier := range allTiers {
			for _, permit := range []bool{false, true} {
				if got, want := table(tier, k, permit), conductor.SensitivityAllows(tier, lt, permit); got != want {
					t.Errorf("table(%v, %v, %v) = %v, conductor says %v", tier, k, permit, got, want)
				}
			}
		}
	}
	for _, tier := range allTiers {
		if table(tier, topology.LaneKindUnresolved, true) {
			t.Errorf("unresolved admitted tier %v", tier)
		}
	}

	seedFreeLane(t, r.Topology(), "lane-a")
	floors := r.CapacityParts().Deps.Floors
	if len(floors) == 0 {
		t.Fatal("no lease floor composed")
	}
	slot := capacity.ProviderSlot{LaneID: "lane-a", LaneType: conductor.LaneExternalAPI}
	req := func(tier provider.SensitivityTier) capacity.ResourceRequest {
		return capacity.ResourceRequest{Sensitivity: tier, DataClass: &tier}
	}
	if ok, why := floors[0].Admit(req(provider.SensitivityPublic), slot); !ok {
		t.Fatalf("public lease refused: %s", why)
	}
	if ok, _ := floors[0].Admit(req(provider.SensitivityRestricted), slot); ok {
		t.Fatal("restricted lease admitted on an external lane")
	}
}
