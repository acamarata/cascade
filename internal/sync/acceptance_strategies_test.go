package sync

// Purpose (this file): the per-strategy resolution assertions the drill
//   makes ON THE ROUND TRIP — not on a direct call to a merge function.
//
// WHY THAT DISTINCTION IS THE POINT. Every strategy already has a unit
//   suite proving it resolves correctly when called. What an acceptance
//   drill adds is that the strategy the ROUND TRIP selects is the one the
//   plan decided, over records that really crossed a wire — a merge
//   chosen wrongly resolves perfectly and still produces the wrong answer.
//
// SPORT: sync/acceptance-drill strategies (ADD) — P1-E17-W4-S38-T5.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// TestEachDomainResolvesUnderItsPlannedStrategy is the verbatim S-38.T2
// table, asserted on what the round trip actually ran.
func TestEachDomainResolvesUnderItsPlannedStrategy(t *testing.T) {
	for _, tc := range []struct {
		domain  storage.DomainID
		subkind string
		fixture string
		want    StrategyName
	}{
		{storage.DomainConfig, "config", "config-delete-vs-write.json", StrategyServerPrimaryLWW},
		{storage.DomainConfig, "accounts", "config-revision-tie-break.json", StrategyMetadataOnly},
		{storage.DomainConfig, "registry", "config-clock-skew-24h.json", StrategyMetadataOnly},
		{storage.DomainMemory, "memory", "memory-concurrent-update.json", StrategyAppendTombstone},
		{storage.DomainContext, "conversation", "memory-double-tombstone.json", StrategyAppendTombstone},
	} {
		t.Run(string(tc.domain)+"/"+tc.subkind, func(t *testing.T) {
			a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
			injectConflict(t, a, b, injectedDomain{tc.domain, tc.subkind, tc.fixture})
			res := syncLeg(t, localCarrier, a, b, tc.domain, tc.subkind, nodes.TierController)
			if res.Strategy != tc.want {
				t.Fatalf("the round trip resolved %s/%s under %q, want the planned %q",
					tc.domain, tc.subkind, res.Strategy, tc.want)
			}
		})
	}
}

// TestTheLosingConfigSideIsJournaledNotDropped is the server-primary rule
// that matters to an operator: the value that went is recorded, with both
// sides' identities, so somebody can see what they lost.
func TestTheLosingConfigSideIsJournaledNotDropped(t *testing.T) {
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	injectConflict(t, a, b, injectedDomain{storage.DomainConfig, "config", "config-delete-vs-write.json"})
	syncLeg(t, localCarrier, a, b, storage.DomainConfig, "config", nodes.TierController)

	entries := b.engine.Conflicts().List()
	if len(entries) == 0 {
		t.Fatal("a server-primary overwrite journaled nothing; the losing value left no trace")
	}
	for _, c := range entries {
		if c.Winner.NodeID == "" && c.Winner.Ref == "" {
			t.Errorf("conflict %q names no winner", c.RecordID)
		}
		if c.Loser.NodeID == "" && c.Loser.Ref == "" {
			t.Errorf("conflict %q names no loser; the discarded side is the one an operator "+
				"is looking for", c.RecordID)
		}
		if c.Resolution == "" {
			t.Errorf("conflict %q records no resolution", c.RecordID)
		}
	}
}

// TestATombstoneWinsOnBothSidesOfTheAppendDomains is the append-merge
// rule, asserted after a round trip in BOTH directions: a delete that only
// survived one direction would resurrect on the next sync.
func TestATombstoneWinsOnBothSidesOfTheAppendDomains(t *testing.T) {
	for _, d := range []injectedDomain{
		{storage.DomainMemory, "memory", "memory-resurrection-suppressed.json"},
		{storage.DomainContext, "conversation", "memory-double-tombstone.json"},
	} {
		t.Run(d.subkind, func(t *testing.T) {
			a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
			injectConflict(t, a, b, d)
			tombstoned := map[string]bool{}
			for _, side := range []*acceptancePeer{a, b} {
				for id, rec := range side.state[string(d.domain)+"/"+d.subkind] {
					if rec.Tombstone {
						tombstoned[id] = true
					}
				}
			}
			if len(tombstoned) == 0 {
				t.Skip("this fixture carries no tombstone; the dominance rule has nothing to say here")
			}
			syncLeg(t, localCarrier, a, b, d.domain, d.subkind, nodes.TierController)
			syncLeg(t, localCarrier, b, a, d.domain, d.subkind, nodes.TierController)

			for id := range tombstoned {
				for _, side := range []*acceptancePeer{a, b} {
					rec, ok := side.state[string(d.domain)+"/"+d.subkind][id]
					if !ok {
						t.Errorf("%s: deleted record %q vanished entirely; a tombstone is the "+
							"record OF a deletion, not its absence", side.name, id)
						continue
					}
					if !rec.Tombstone {
						t.Errorf("%s: record %q came back to life across the round trip", side.name, id)
					}
				}
			}
		})
	}
}

// TestTheBlobUnionIsLossless covers the domain whose merge cannot conflict
// on a record id: two sides with disjoint content addresses must end with
// every address from both, and an unadmitted blob is an error rather than
// a quietly missing object.
func TestTheBlobUnionIsLossless(t *testing.T) {
	a := map[string]BlobRef{"aa": {Address: "aa", Admitted: true}, "shared": {Address: "shared", Admitted: true}}
	b := map[string]BlobRef{"bb": {Address: "bb", Admitted: true}, "shared": {Address: "shared", Admitted: true}}

	e := newAcceptancePeer(t, "server").engine
	res, err := e.Merge(context.Background(), MergeRequest{
		Domain: storage.DomainBlobs, Subkind: "blobs", PeerTier: nodes.TierController,
		ServerBlobs: a, LocalBlobs: b,
	})
	if err != nil {
		t.Fatalf("the blob union refused a clean pair: %v", err)
	}
	if res.Strategy != StrategyContentAddressUnion {
		t.Fatalf("blobs resolved under %q, want the content-address union", res.Strategy)
	}
	for _, addr := range []string{"aa", "bb", "shared"} {
		if _, ok := res.Blobs[addr]; !ok {
			t.Errorf("the union lost address %q", addr)
		}
	}
	if len(res.Blobs) != 3 {
		t.Errorf("the union holds %d addresses, want 3 (the shared one counted once)", len(res.Blobs))
	}

	// An unadmitted blob is refused, which is what stops a corrupted
	// transfer from reading as a blob that simply was not there.
	if _, err := e.Merge(context.Background(), MergeRequest{
		Domain: storage.DomainBlobs, Subkind: "blobs", PeerTier: nodes.TierController,
		ServerBlobs: map[string]BlobRef{"bad": {Address: "bad"}}, LocalBlobs: b,
	}); err == nil {
		t.Error("the union admitted a blob staging never verified")
	}
}

// TestADomainNobodyMappedNeverMerges is the fail-closed edge of the table:
// an unmapped domain has no default merge, because a default here means
// picking a resolution rule for data whose rule nobody decided.
func TestADomainNobodyMappedNeverMerges(t *testing.T) {
	e := newAcceptancePeer(t, "laptop").engine
	_, err := e.Merge(context.Background(), MergeRequest{
		Domain: storage.DomainConfig, Subkind: "not-a-registered-subkind",
		PeerTier: nodes.TierController,
		Server:   map[string]Record{"x": {Tier: egress.TierInternal}},
	})
	if err == nil {
		t.Fatal("an unmapped domain was merged under some default strategy")
	}
}
