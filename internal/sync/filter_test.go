package sync

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
)

// TestSyncSensitivityFilterBeforeSerialize is the ticket contract's named
// acceptance test (R-21.223 §G.2): local-only and restricted records
// never serialize, even inside an otherwise-synced domain.
func TestSyncSensitivityFilterBeforeSerialize(t *testing.T) {
	for _, tier := range []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted} {
		res := Admit(Record{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-filter", Tier: tier})
		if res.Admitted {
			t.Fatalf("tier %q must never be admitted for serialization, even in a synced domain", tier)
		}
	}
	res := Admit(Record{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-filter-ok", Tier: egress.TierInternal})
	if !res.Admitted {
		t.Fatalf("an internal-tier record in a synced domain must serialize, got Reason=%q", res.Reason)
	}
}

func TestAdmitLocalOnlyRecordNeverSerializes(t *testing.T) {
	res := Admit(Record{Domain: storage.DomainConfig, Subkind: "config", ID: "rec-1", Tier: egress.TierLocalOnly})
	if res.Admitted {
		t.Fatal("a local-only-tiered record must never be admitted, even in a synced domain")
	}
	if res.Reason != "sensitivity-local-only" {
		t.Fatalf("Reason = %q, want sensitivity-local-only", res.Reason)
	}
}

func TestAdmitRestrictedRecordNeverSerializes(t *testing.T) {
	res := Admit(Record{Domain: storage.DomainConfig, Subkind: "config", ID: "rec-2", Tier: egress.TierRestricted})
	if res.Admitted {
		t.Fatal("a restricted-tiered record must never be admitted, even in a synced domain")
	}
}

func TestAdmitUnregisteredDomainRefused(t *testing.T) {
	res := Admit(Record{Domain: storage.DomainAudit, Subkind: "audit", ID: "rec-3", Tier: egress.TierInternal})
	if res.Admitted {
		t.Fatal("an unregistered domain must refuse, never admit by default")
	}
	if res.Reason != "domain-unregistered" {
		t.Fatalf("Reason = %q, want domain-unregistered", res.Reason)
	}
}

func TestAdmitVaultDomainAlwaysRefused(t *testing.T) {
	// The vault is structurally absent from the registry (domains.go):
	// even a caller that mistakenly builds a Record over DomainSecrets
	// with a public tier is refused, because the lookup itself fails.
	res := Admit(Record{Domain: storage.DomainSecrets, Subkind: "vault", ID: "rec-vault", Tier: egress.TierPublic})
	if res.Admitted {
		t.Fatal("a vault-domain record must never be admitted regardless of its declared tier")
	}
}

func TestAdmitInternalRecordInSyncedDomainAdmitted(t *testing.T) {
	res := Admit(Record{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-4", Tier: egress.TierInternal})
	if !res.Admitted {
		t.Fatalf("an internal-tier record in a synced domain must be admitted, got Reason=%q", res.Reason)
	}
}

func TestFilterBatchSplitsAdmittedAndExcluded(t *testing.T) {
	recs := []Record{
		{Domain: storage.DomainMemory, Subkind: "memory", ID: "rec-a", Tier: egress.TierInternal},
		{Domain: storage.DomainConfig, Subkind: "accounts", ID: "rec-b", Tier: egress.TierRestricted, PolicyVersion: 3},
	}
	admitted, excluded := FilterBatch(recs)
	if len(admitted) != 1 || admitted[0].ID != "rec-a" {
		t.Fatalf("admitted = %+v, want just rec-a", admitted)
	}
	if len(excluded) != 1 || excluded[0].RecordID != "rec-b" || excluded[0].PolicyVersion != 3 {
		t.Fatalf("excluded = %+v, want rec-b at policy version 3", excluded)
	}
}
