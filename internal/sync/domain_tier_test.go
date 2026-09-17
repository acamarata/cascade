package sync

// Purpose (this file): the domain x tier table, cell by cell, against the
//   committed golden.
// WHY A GOLDEN AND A CELL TEST BOTH: the golden makes a change to the
//   table a visible diff in review; the per-cell assertions state WHY each
//   cell is what it is, which a golden cannot.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// TestDomainTierGolden pins the whole table.
func TestDomainTierGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/domain_tier.golden")
	if err != nil {
		t.Fatalf("reading the golden: %v", err)
	}
	if got := domainTierTable(); got != string(want) {
		t.Errorf("the domain x tier table changed.\n got:\n%s\nwant:\n%s\n"+
			"If this change is intended, it is a decision about where personal data goes — "+
			"update the golden deliberately, not to make the test pass.", got, want)
	}
}

// TestEveryDomainTierCell states each cell and the reason for it.
func TestEveryDomainTierCell(t *testing.T) {
	for _, tc := range []struct {
		tier    nodes.Tier
		domain  storage.DomainID
		subkind string
		want    bool
		why     string
	}{
		{nodes.TierController, storage.DomainConfig, "config", true, "the controller holds everything anyway"},
		{nodes.TierController, storage.DomainMemory, "memory", true, "the controller holds everything anyway"},
		{nodes.TierController, storage.DomainContext, "conversation", true, "the controller holds everything anyway"},
		{nodes.TierController, storage.DomainConfig, "accounts", true, "metadata only, and the controller is where it lives"},

		{nodes.TierWorkerTrusted, storage.DomainConfig, "config", true, "a worker needs configuration to run work"},
		{nodes.TierWorkerTrusted, storage.DomainConfig, "phase-state", true, "a worker needs to know what the work IS"},
		{nodes.TierWorkerTrusted, storage.DomainBlobs, "blobs", true, "a worker needs the blobs the work reads"},
		{nodes.TierWorkerTrusted, storage.DomainConfig, "registry", true, "a worker needs provider metadata"},
		{nodes.TierWorkerTrusted, storage.DomainMemory, "memory", false,
			"a disposable machine does not get a standing copy of somebody's memory"},
		{nodes.TierWorkerTrusted, storage.DomainContext, "conversation", false,
			"a disposable machine does not get a standing copy of somebody's conversations"},
		{nodes.TierWorkerTrusted, storage.DomainConfig, "accounts", false,
			"running work needs no account metadata"},

		{nodes.TierPairedDevice, storage.DomainConfig, "config", false, "nothing syncs to a paired device in P1"},
		{nodes.TierPairedDevice, storage.DomainBlobs, "blobs", false, "nothing syncs to a paired device in P1"},
	} {
		t.Run(string(tc.tier)+"/"+tc.subkind, func(t *testing.T) {
			if got := EligibleForTier(tc.domain, tc.subkind, tc.tier); got != tc.want {
				t.Errorf("EligibleForTier = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestUnlistedDomainTierNoSync is the fail-closed rule, and it is the one
// that makes a closed table worth having: everything nobody decided is no.
func TestUnlistedDomainTierNoSync(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  storage.DomainID
		subkind string
		tier    nodes.Tier
	}{
		{"unregistered domain", storage.DomainAudit, "audit", nodes.TierController},
		{"unregistered subkind", storage.DomainConfig, "not-a-subkind", nodes.TierController},
		{"unknown tier", storage.DomainConfig, "config", nodes.Tier("archivist")},
		{"the zero tier", storage.DomainConfig, "config", nodes.Tier("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if EligibleForTier(tc.domain, tc.subkind, tc.tier) {
				t.Error("an unlisted pair was permitted to sync")
			}
		})
	}
}

// TestEligibleDomainsMatchesTheTable joins the two surfaces: what `sync
// status` would report must be what the gate actually permits.
func TestEligibleDomainsMatchesTheTable(t *testing.T) {
	for _, tier := range []nodes.Tier{nodes.TierController, nodes.TierWorkerTrusted, nodes.TierPairedDevice} {
		for _, subkind := range EligibleDomains(tier) {
			var found bool
			for _, dc := range AllCoreClasses() {
				if dc.Subkind == subkind && EligibleForTier(dc.Domain, dc.Subkind, tier) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: EligibleDomains reports %q, which the gate refuses", tier, subkind)
			}
		}
	}
	if got := EligibleDomains(nodes.TierPairedDevice); len(got) != 0 {
		t.Errorf("a paired device reports %v syncable domains, want none in P1", got)
	}
}
