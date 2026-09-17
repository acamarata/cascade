package sync

// Purpose (this file): the three gates a record passes before it is
//   serialized for a peer, and the rule that none of them ever widens.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// syncable builds a record that passes every gate for a controller.
func syncable() Record {
	return Record{
		Domain: storage.DomainConfig, Subkind: "config", ID: "k",
		Tier: egress.TierInternal,
	}
}

// TestEveryGateCanRefuseOnItsOwn proves the three gates are independent.
// A test that only ever tripped one would pass against an implementation
// that checked one.
func TestEveryGateCanRefuseOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rec    Record
		tier   nodes.Tier
		reason string
	}{
		{"unregistered domain", func() Record {
			r := syncable()
			r.Subkind = "invented"
			return r
		}(), nodes.TierController, "domain-unregistered"},
		{"tier not permitted", func() Record {
			r := syncable()
			r.Domain, r.Subkind = storage.DomainMemory, "memory"
			return r
		}(), nodes.TierWorkerTrusted, "tier-not-permitted"},
		{"record is local-only", func() Record {
			r := syncable()
			r.Tier = egress.TierLocalOnly
			return r
		}(), nodes.TierController, "sensitivity-local-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Eligible(tc.rec, tc.tier)
			if got.Eligible {
				t.Fatal("a record that should have been refused was eligible")
			}
			if got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
		})
	}
}

// TestAnOrdinaryRecordIsEligible keeps the rules above from being "always
// refuse", which would pass every test in this file.
func TestAnOrdinaryRecordIsEligible(t *testing.T) {
	if got := Eligible(syncable(), nodes.TierController); !got.Eligible {
		t.Fatalf("an ordinary config record was refused: %q", got.Reason)
	}
}

// TestRestrictedNeedsWorkerTrusted pins 06 §5.22's tier gate on the
// record's OWN sensitivity, separately from the domain x tier table.
func TestRestrictedNeedsWorkerTrusted(t *testing.T) {
	rec := syncable()
	rec.Tier = egress.TierRestricted

	if got := Eligible(rec, nodes.TierController); !got.Eligible {
		t.Errorf("a restricted record was refused to the controller: %q", got.Reason)
	}
	if got := Eligible(rec, nodes.TierWorkerTrusted); !got.Eligible {
		t.Errorf("a restricted record was refused to a worker-trusted peer: %q", got.Reason)
	}
	if got := Eligible(rec, nodes.TierPairedDevice); got.Eligible {
		t.Error("a restricted record reached a paired device")
	}
}

// TestNoGateEverWidens is the rule stated as a property: for every
// (domain, subkind, tier) the gate permits, the domain x tier table must
// also permit it and the record's own tier must allow it. There is no path
// through Eligible that says yes where a narrower rule said no.
func TestNoGateEverWidens(t *testing.T) {
	tiers := []nodes.Tier{nodes.TierController, nodes.TierWorkerTrusted, nodes.TierPairedDevice, nodes.Tier("")}
	sensitivities := []egress.SensitivityTier{
		egress.TierPublic, egress.TierInternal, egress.TierRestricted, egress.TierLocalOnly, egress.SensitivityTier(""),
	}
	for _, dc := range AllCoreClasses() {
		for _, tier := range tiers {
			for _, sens := range sensitivities {
				rec := Record{Domain: dc.Domain, Subkind: dc.Subkind, ID: "x", Tier: sens}
				if !Eligible(rec, tier).Eligible {
					continue
				}
				if !EligibleForTier(dc.Domain, dc.Subkind, tier) {
					t.Errorf("%s/%s to %q: eligible despite the tier table refusing it", dc.Domain, dc.Subkind, tier)
				}
				if r := sens.Resolve(); r == egress.TierLocalOnly {
					t.Errorf("%s/%s to %q: a local-only record was eligible", dc.Domain, dc.Subkind, tier)
				}
			}
		}
	}
}

// TestTheZeroSensitivityResolvesRestrictively is the fail-closed default
// on the record side: a record whose tier nobody set must not be treated
// as public.
func TestTheZeroSensitivityResolvesRestrictively(t *testing.T) {
	rec := syncable()
	rec.Tier = egress.SensitivityTier("")
	if got := Eligible(rec, nodes.TierPairedDevice); got.Eligible {
		t.Error("a record with no sensitivity set reached a paired device")
	}
}
