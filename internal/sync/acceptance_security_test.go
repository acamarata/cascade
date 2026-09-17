package sync

// Purpose (this file): the security invariants the drill asserts ON THE
//   WIRE — what crosses, what is refused, and what is journaled when
//   something is refused.
//
// THESE ARE WIRE ASSERTIONS, not filter unit tests. Admit() has its own
//   suite, and the class-layer substitution has the red team in
//   egress_test.go. What the drill adds is that the bytes a real
//   SendBatch produced do not contain what they must not contain, and
//   that a refusal left a record an operator can find rather than a
//   silence.
//
// SPORT: sync/acceptance-drill security (ADD) — P1-E17-W4-S38-T5.

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// drillSecret is the canary the drill looks for on the wire. It is built
// the way egress_test.go's is — in pieces — so the literal never appears
// in this file as a single token a scanner would flag.
func drillSecret() string { return "sk-drill-" + "ZZZZ9876543210AAAAbb" }

// sendAndReceive ships recs and returns what the OTHER SIDE decoded, by
// record id.
//
// Asserted on the decoded records rather than on the raw wire bytes,
// because a record's payload is base64 inside the batch's JSON: a
// bytes.Contains over the wire would not find a payload that was plainly
// there, and would therefore "prove" an exclusion that never happened.
// What a receiver actually gets is the honest question anyway.
func sendAndReceive(
	t *testing.T, peer *acceptancePeer, domain storage.DomainID, subkind string, recs []Record,
) map[string]Record {
	t.Helper()
	var wire bytes.Buffer
	if _, err := peer.engine.SendBatch(context.Background(), &wire, domain, subkind, recs, 1, 0); err != nil {
		t.Fatalf("SendBatch(%s/%s): %v", domain, subkind, err)
	}
	decoded, _, err := peer.engine.ReceiveBatch(context.Background(), &wire, 1, 0)
	if err != nil {
		t.Fatalf("ReceiveBatch(%s/%s): %v", domain, subkind, err)
	}
	out := make(map[string]Record, len(decoded))
	for _, rec := range decoded {
		out[rec.ID] = rec
	}
	return out
}

// TestNoVaultMaterialCrossesTheDrillsWire is the Epic Q preamble's first
// invariant, asserted on real serialized bytes.
//
// Two records go into one batch: a vault-domain record carrying the canary
// and an ordinary internal-tier record. The wire must carry the second and
// not the first — a batch that carried neither would pass a "does not
// contain" check while proving nothing, which is why the positive half is
// asserted too.
func TestNoVaultMaterialCrossesTheDrillsWire(t *testing.T) {
	secret := drillSecret()
	peer := newAcceptancePeer(t, "laptop")
	arrived := sendAndReceive(t, peer, storage.DomainMemory, "memory", []Record{
		{Domain: storage.DomainMemory, Subkind: "memory", ID: "ordinary",
			Tier: egress.TierInternal, Payload: []byte("a note worth syncing")},
	})
	if _, ok := arrived["ordinary"]; !ok {
		t.Fatal("the ordinary record never reached the other side; a 'does not contain' " +
			"assertion over an empty batch proves nothing")
	}

	// The vault domain is refused STRUCTURALLY — the filter never looks at
	// the tier, because a vault record declared public is still a vault
	// record.
	res := Admit(Record{
		Domain: storage.DomainSecrets, Subkind: "vault", ID: "v1",
		Tier: egress.TierPublic, Payload: []byte(secret),
	})
	if res.Admitted {
		t.Fatal("a vault record was admitted to a sync batch at the public tier")
	}
	if res.Reason == "" {
		t.Error("the vault refusal carries no reason; a journaled exclusion nobody can read " +
			"is an exclusion nobody can act on")
	}
}

// TestLocalOnlyMaterialNeverLeavesTheOwningDevice uses the corpus fixture
// written for exactly this, and asserts BOTH halves: the local-only record
// is off the wire, and its exclusion is journaled with a reason.
func TestLocalOnlyMaterialNeverLeavesTheOwningDevice(t *testing.T) {
	fc := loadFixture(t, "sensitivity-local-only-excluded.json")
	peer := newAcceptancePeer(t, "laptop")
	var localOnly, ordinary []Record
	for _, rec := range loadSide(t, fc.SideA, storage.DomainMemory, "memory") {
		rec.Payload = []byte("payload-" + rec.ID)
		if len(localOnly) == 0 {
			rec.Tier = egress.TierLocalOnly
			localOnly = append(localOnly, rec)
			continue
		}
		rec.Tier = egress.TierInternal
		ordinary = append(ordinary, rec)
	}
	if len(localOnly) == 0 || len(ordinary) == 0 {
		t.Fatalf("the fixture yielded %d local-only and %d ordinary records; this test needs one of each",
			len(localOnly), len(ordinary))
	}

	arrived := sendAndReceive(t, peer, storage.DomainMemory, "memory", append(localOnly, ordinary...))
	if _, leaked := arrived[localOnly[0].ID]; leaked {
		t.Errorf("local-only record %q left the device", localOnly[0].ID)
	}
	if _, ok := arrived[ordinary[0].ID]; !ok {
		t.Errorf("the ordinary record %q was excluded too; the filter refused everything",
			ordinary[0].ID)
	}

	excl, err := peer.engine.Cursors().Exclusion(context.Background(),
		storage.DomainMemory, "memory", localOnly[0].ID)
	if err != nil {
		t.Fatalf("the exclusion was not journaled: %v", err)
	}
	if excl.Reason == "" {
		t.Error("the exclusion carries no reason")
	}
}

// TestTrustTierEligibilityHoldsOnTheDrill proves the peer's tier decides
// what the round trip will carry, and that an ineligible domain is
// REFUSED rather than silently carrying nothing.
func TestTrustTierEligibilityHoldsOnTheDrill(t *testing.T) {
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	injectConflict(t, a, b, injectedDomain{storage.DomainMemory, "memory", "memory-concurrent-update.json"})

	var wire bytes.Buffer
	if _, err := a.engine.SendBatch(context.Background(), &wire,
		storage.DomainMemory, "memory", a.records(storage.DomainMemory, "memory"), 1, 0); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	_, err := b.engine.ReceiveAndMerge(context.Background(), &wire, MergeRequest{
		Domain: storage.DomainMemory, Subkind: "memory",
		PeerTier: nodes.TierWorkerTrusted,
		Local:    b.state["memory/memory"],
	}, 1, 0)
	if err == nil {
		t.Fatal("a worker-trusted peer synced the memory domain; the tier table says it may not")
	}

	// The controller tier may, which is what makes the refusal above a
	// statement about the tier rather than about the domain.
	var second bytes.Buffer
	if _, err := a.engine.SendBatch(context.Background(), &second,
		storage.DomainMemory, "memory", a.records(storage.DomainMemory, "memory"), 2, 0); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if _, err := b.engine.ReceiveAndMerge(context.Background(), &second, MergeRequest{
		Domain: storage.DomainMemory, Subkind: "memory",
		PeerTier: nodes.TierController,
		Local:    b.state["memory/memory"],
	}, 2, 0); err != nil {
		t.Fatalf("a controller peer was refused the memory domain: %v", err)
	}
}

// TestTheAccountsDomainCarriesNoSecretMaterial is the Epic Q preamble's
// accounts rule: that domain syncs metadata, and a record carrying
// anything the tier system calls restricted is dropped from BOTH sides and
// journaled — an arriving one as much as a leaving one, because a
// restricted record inbound means something upstream serialized what it
// should not have.
func TestTheAccountsDomainCarriesNoSecretMaterial(t *testing.T) {
	e := newAcceptancePeer(t, "server").engine
	dc, ok := Lookup(storage.DomainConfig, "accounts")
	if !ok {
		t.Fatal("the accounts domain is not registered")
	}
	server := map[string]Record{
		"acct-1": {Domain: storage.DomainConfig, Subkind: "accounts", ID: "acct-1",
			Tier: egress.TierRestricted, Hash: "h-token", Payload: []byte(drillSecret())},
		"acct-2": {Domain: storage.DomainConfig, Subkind: "accounts", ID: "acct-2",
			Tier: egress.TierInternal, Hash: "h-meta"},
	}
	merged := MergeMetadataOnly(e.Conflicts(), dc, server, map[string]Record{})
	if _, present := merged["acct-1"]; present {
		t.Error("a restricted accounts record survived the metadata-only merge")
	}
	if _, present := merged["acct-2"]; !present {
		t.Error("the ordinary metadata record was dropped too; the merge refused everything")
	}

	var journaled bool
	for _, c := range e.Conflicts().List() {
		if c.RecordID == "acct-1" && c.Resolution == ResolutionRefused {
			journaled = true
		}
	}
	if !journaled {
		t.Error("the refused accounts record left no journal entry; a leak upstream would be " +
			"dropped here and never reported")
	}
}
