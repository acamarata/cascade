package sync

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
)

func TestDataClassFilterRestrictedBranches(t *testing.T) {
	for _, tier := range []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted, egress.TierInternal, egress.TierPublic, 9} {
		rec := syncable()
		rec.Tier = tier
		got := Admit(rec)
		if got.Admitted != (tier == egress.TierInternal || tier == egress.TierPublic) {
			t.Fatalf("tier %v: %+v", tier, got)
		}
		if !got.Admitted && got.Reason == "" {
			t.Fatal("missing refusal reason")
		}
	}
}

func TestDataClassEligibilityRestrictedBranches(t *testing.T) {
	for _, tier := range []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted, egress.TierInternal, egress.TierPublic, 9} {
		rec := syncable()
		rec.Tier = tier
		got := Eligible(rec, nodes.TierWorkerTrusted)
		if got.Eligible != (tier == egress.TierInternal || tier == egress.TierPublic || tier == egress.TierRestricted) {
			t.Fatalf("tier %v: %+v", tier, got)
		}
		if !got.Eligible && got.Reason == "" {
			t.Fatal("missing refusal reason")
		}
	}
	rec := syncable()
	rec.Tier = egress.TierRestricted
	if !Eligible(rec, nodes.TierWorkerTrusted).Eligible {
		t.Fatal("trusted peer lost restricted admission")
	}
}

func TestDataClassMetadataRestrictedBranches(t *testing.T) {
	for _, tier := range []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted, egress.TierInternal, egress.TierPublic, 9} {
		journal := &ConflictJournal{}
		rec := arec("record", tier, 1, "server")
		got := MergeMetadataOnly(journal, accountsDomain(t), map[string]Record{rec.ID: rec}, nil)
		_, kept := got[rec.ID]
		if kept != (tier == egress.TierInternal || tier == egress.TierPublic) {
			t.Fatalf("tier %v: %+v", tier, got)
		}
		if !kept && journal.Len() != 1 {
			t.Fatal("refusal not recorded")
		}
	}
}
