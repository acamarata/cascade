package sync

// Purpose (this file): conflict injection across every domain class, and
//   the dress-rehearsal lane that runs the whole drill script over two
//   local engines.
//
// THE INJECTION SET IS THE SPIKE CORPUS, not new fixtures. The M/S-27.T5
//   adversarial cases were built to break the merges and are digest-gated
//   (fixtures_test.go), so a drill that invented its own conflicts would
//   be proving the merges against inputs chosen after the merges existed.
//
// SPORT: sync/acceptance-drill conflicts (ADD) — P1-E17-W4-S38-T5.

import (
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// injectedDomain is one domain class the drill conflicts in.
type injectedDomain struct {
	domain  storage.DomainID
	subkind string
	fixture string
}

// injectionSet is every RECORD domain class the drill injects into, paired
// with the spike case that breaks it.
//
// Blobs and phase-state are injected by their own assertions
// (acceptance_strategies_test.go): the blob union is keyed by content
// address and cannot conflict on a record id, and phase state is git's
// answer rather than a record merge. Listing them here with a record
// injection would be pretending they work the same way.
func injectionSet() []injectedDomain {
	return []injectedDomain{
		{storage.DomainConfig, "config", "config-delete-vs-write.json"},
		{storage.DomainConfig, "accounts", "config-revision-tie-break.json"},
		{storage.DomainConfig, "registry", "config-clock-skew-24h.json"},
		{storage.DomainMemory, "memory", "memory-concurrent-update.json"},
		{storage.DomainContext, "conversation", "memory-double-tombstone.json"},
	}
}

// injectConflict seeds both peers from one fixture's two sides.
func injectConflict(t *testing.T, a, b *acceptancePeer, d injectedDomain) {
	t.Helper()
	fc := loadFixture(t, d.fixture)
	for _, rec := range loadSide(t, fc.SideA, d.domain, d.subkind) {
		a.put(sendable(rec))
	}
	for _, rec := range loadSide(t, fc.SideB, d.domain, d.subkind) {
		b.put(sendable(rec))
	}
}

// sendable gives a fixture record the two fields the WIRE needs and the
// merge corpus does not carry: a sensitivity tier and a payload.
//
// The tier is explicit rather than left zero because an unset tier
// RESOLVES TO RESTRICTED (egress/class.go — an unrecognised
// classification is one the code cannot reason about, so it fails
// closed). Left unset, every fixture record would be filtered out of
// every batch and the drill would assert convergence over an empty wire,
// which is the most confident-looking way for an acceptance test to
// prove nothing. The security fixtures set their own tiers
// (acceptance_security_test.go) precisely so this default cannot mask
// them.
func sendable(rec Record) Record {
	if rec.Tier == "" {
		rec.Tier = egress.TierInternal
	}
	if len(rec.Payload) == 0 {
		rec.Payload = []byte(rec.Hash)
	}
	return rec
}

// runDrillScript is THE drill: inject a conflict into every record domain
// class, sync both directions, and hand back the two peers for the
// per-strategy and security assertions to read.
//
// The rehearsal and the real drill call this same function. The only
// difference between them is where the peers live.
func runDrillScript(t *testing.T, carry wireCarrier, a, b *acceptancePeer, tier nodes.Tier) {
	t.Helper()
	for _, d := range injectionSet() {
		injectConflict(t, a, b, d)
		// Laptop → server, then server → laptop. Both directions, because
		// a merge that converged in one and not the other would leave two
		// machines disagreeing with no way to tell which was right.
		syncLeg(t, carry, a, b, d.domain, d.subkind, tier)
		syncLeg(t, carry, b, a, d.domain, d.subkind, tier)
	}
}

// TestSyncRoundTripDressRehearsalLoopback is the ticket's named
// unconditional lane. It runs the WHOLE drill script between two local
// engines, so the only thing left untested on the real run is the peer.
//
// It is not the acceptance and never claims to be: the real drill is
// acceptance_evidence_test.go's, against an enrolled second machine.
func TestSyncRoundTripDressRehearsalLoopback(t *testing.T) {
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	runDrillScript(t, localCarrier, a, b, nodes.TierController)

	// Both sides converged, on every domain the script touched.
	assertConverged(t, a, b)
	// And something actually happened: a script that injected nothing
	// would converge trivially.
	if a.engine.Conflicts().Len() == 0 && b.engine.Conflicts().Len() == 0 {
		t.Error("the drill journaled no conflicts at all; the injection set did not collide")
	}
}

// assertConverged proves both sides hold the same records, by id and by
// content hash, for every domain the drill touched.
//
// By HASH as well as by id, because two sides holding the same set of ids
// with different contents is the divergence that looks like convergence
// from a length check.
func assertConverged(t *testing.T, a, b *acceptancePeer) {
	t.Helper()
	for _, d := range injectionSet() {
		key := string(d.domain) + "/" + d.subkind
		left, right := a.state[key], b.state[key]
		if len(left) != len(right) {
			t.Errorf("%s: laptop holds %d records, server holds %d", key, len(left), len(right))
			continue
		}
		for id, rec := range left {
			other, ok := right[id]
			if !ok {
				t.Errorf("%s: record %q is on the laptop and not on the server", key, id)
				continue
			}
			if other.Hash != rec.Hash {
				t.Errorf("%s: record %q has hash %q on the laptop and %q on the server",
					key, id, rec.Hash, other.Hash)
			}
			if other.Tombstone != rec.Tombstone {
				t.Errorf("%s: record %q is a tombstone on one side only", key, id)
			}
		}
	}
}

// TestNoSideLosesARecord is the no-silent-loss assertion, stated over the
// ids each side held BEFORE the drill rather than after it.
//
// Asserting it after the merge would be asserting that the merge agrees
// with itself. What has to hold is that every id either side ever had is
// still addressable — as a live record or as a tombstone, which is a
// record of a deletion rather than the absence of one.
func TestNoSideLosesARecord(t *testing.T) {
	a, b := newAcceptancePeer(t, "laptop"), newAcceptancePeer(t, "server")
	before := map[string]map[string]bool{}
	for _, d := range injectionSet() {
		injectConflict(t, a, b, d)
		key := string(d.domain) + "/" + d.subkind
		before[key] = map[string]bool{}
		for id := range a.state[key] {
			before[key][id] = true
		}
		for id := range b.state[key] {
			before[key][id] = true
		}
		syncLeg(t, localCarrier, a, b, d.domain, d.subkind, nodes.TierController)
		syncLeg(t, localCarrier, b, a, d.domain, d.subkind, nodes.TierController)
	}

	for key, ids := range before {
		for id := range ids {
			if _, ok := a.state[key][id]; !ok {
				t.Errorf("%s: record %q vanished from the laptop", key, id)
			}
			if _, ok := b.state[key][id]; !ok {
				t.Errorf("%s: record %q vanished from the server", key, id)
			}
		}
	}
}
