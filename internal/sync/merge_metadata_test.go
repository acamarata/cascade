package sync

// Purpose (this file): the metadata-only merge, and the structural reason
//   a credential cannot ride it.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
)

// accountsDomain is the registered accounts class.
func accountsDomain(t *testing.T) DomainClass {
	t.Helper()
	dc, ok := Lookup(storage.DomainConfig, "accounts")
	if !ok {
		t.Fatal("the accounts domain is not registered")
	}
	return dc
}

// arec builds one accounts record at a sensitivity tier.
func arec(id string, tier egress.SensitivityTier, revision uint64, node string) Record {
	return Record{
		Domain: storage.DomainConfig, Subkind: "accounts", ID: id, Tier: tier,
		Hash:  "h-" + id,
		Order: OrderKey{Revision: revision, NodeID: node},
	}
}

// TestARecordThatMustNotLeaveIsRefusedAndJournaled is the enforcement. A
// silent omission would look identical to the record never having been
// written, and nobody would know their account metadata was not syncing.
func TestARecordThatMustNotLeaveIsRefusedAndJournaled(t *testing.T) {
	dc := accountsDomain(t)
	for _, tier := range []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted} {
		t.Run(string(tier), func(t *testing.T) {
			journal := &ConflictJournal{}
			merged := MergeMetadataOnly(journal, dc,
				map[string]Record{"keep": arec("keep", egress.TierInternal, 1, "server")},
				map[string]Record{"drop": arec("drop", tier, 1, "laptop")},
			)
			if _, present := merged["drop"]; present {
				t.Fatalf("a %s record replicated", tier)
			}
			if _, present := merged["keep"]; !present {
				t.Error("an admissible record was dropped alongside it")
			}
			entries := journal.List()
			if len(entries) != 1 || entries[0].Resolution != ResolutionRefused {
				t.Fatalf("the drop was not journaled as a refusal: %+v", entries)
			}
			if !strings.Contains(entries[0].Detail, "sensitivity") {
				t.Errorf("the journal entry does not say why: %q", entries[0].Detail)
			}
		})
	}
}

// TestARestrictedRecordFromTheServerIsAlsoRefused is the half that is easy
// to leave out. A restricted record arriving FROM the server means
// something upstream serialized what it should not have; accepting it
// quietly would spread the fault rather than surface it.
func TestARestrictedRecordFromTheServerIsAlsoRefused(t *testing.T) {
	dc := accountsDomain(t)
	journal := &ConflictJournal{}
	merged := MergeMetadataOnly(journal, dc,
		map[string]Record{"leaked": arec("leaked", egress.TierRestricted, 9, "server")},
		map[string]Record{},
	)
	if _, present := merged["leaked"]; present {
		t.Fatal("a restricted record arriving from the server was accepted")
	}
	if journal.Len() != 1 {
		t.Errorf("%d journal entr(y/ies) for an inbound leak, want 1", journal.Len())
	}
}

// TestTheMergedRecordTypeHasNowhereToPutASecret is the structural claim
// this strategy rests on, asserted over the FIELD SET so a field added
// later fails here rather than shipping a credential.
//
// It deliberately does not search for a sample secret: no code path puts a
// credential value into a Record, so such a search could never fail and
// would be decoration (the same shape R-14.268 removed elsewhere).
func TestTheMergedRecordTypeHasNowhereToPutASecret(t *testing.T) {
	reviewed := map[string]bool{
		"Domain": true, "Subkind": true, "ID": true, "Tier": true,
		"PolicyVersion": true, "Payload": true,
		"Order": true, "Hash": true, "Tombstone": true, "Vector": true,
	}
	typ := reflect.TypeOf(Record{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !reviewed[name] {
			t.Errorf("Record grew field %q. Account and registry records ride this type to another "+
				"machine, so a field able to hold credential material would ship one. Add it to the "+
				"reviewed set once you have checked what it carries.", name)
		}
	}
}

// TestMetadataMergeStillTakesTheServersSide keeps the refusal above from
// being the only thing this strategy does.
func TestMetadataMergeStillTakesTheServersSide(t *testing.T) {
	dc := accountsDomain(t)
	journal := &ConflictJournal{}
	merged := MergeMetadataOnly(journal, dc,
		map[string]Record{"k": arec("k", egress.TierInternal, 9, "server")},
		map[string]Record{"k": arec("k", egress.TierInternal, 8, "laptop")},
	)
	if merged["k"].Order.NodeID != "server" {
		t.Errorf("merged record came from %q, want the server", merged["k"].Order.NodeID)
	}
	if journal.Len() != 1 || journal.List()[0].Resolution != ResolutionServerWon {
		t.Errorf("the discarded local write was not journaled: %+v", journal.List())
	}
}
