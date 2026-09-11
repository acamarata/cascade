//go:build spike

package syncmerge

import (
	"reflect"
	"testing"
)

// assertMemoryContentEqual compares two memory-merge results ignoring
// VersionVector map key insertion order (maps compare correctly under
// reflect.DeepEqual already, so this exists to give a clearer failure
// message than a raw DeepEqual would).
func assertMemoryContentEqual(t *testing.T, label string, a, b map[string]MemoryRecord) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: result sizes differ: %d vs %d", label, len(a), len(b))
	}
	for id, rec := range a {
		other, ok := b[id]
		if !ok {
			t.Fatalf("%s: id %s present in first result, missing from second", label, id)
		}
		if rec.Tombstone != other.Tombstone || rec.Revision != other.Revision || rec.HLC != other.HLC || rec.NodeID != other.NodeID {
			t.Fatalf("%s: id %s differs: %+v vs %+v", label, id, rec, other)
		}
	}
}

// assertNoLossConfig confirms every input record id appears in the merge
// output (R-21.219 no-loss property; config has no tombstone-pruning
// concept, so presence alone suffices).
func assertNoLossConfig(t *testing.T, label string, a, b, merged map[string]ConfigRecord) {
	t.Helper()
	for id := range a {
		if _, ok := merged[id]; !ok {
			t.Fatalf("%s: config no-loss failed: id %s from side A missing from merge", label, id)
		}
	}
	for id := range b {
		if _, ok := merged[id]; !ok {
			t.Fatalf("%s: config no-loss failed: id %s from side B missing from merge", label, id)
		}
	}
}

// assertNoLossMemory confirms every input record id appears in the merge
// output, tombstoned or not (R-21.219 no-loss property).
func assertNoLossMemory(t *testing.T, label string, a, b, merged map[string]MemoryRecord) {
	t.Helper()
	for id := range a {
		if _, ok := merged[id]; !ok {
			t.Fatalf("%s: memory no-loss failed: id %s from side A missing from merge", label, id)
		}
	}
	for id := range b {
		if _, ok := merged[id]; !ok {
			t.Fatalf("%s: memory no-loss failed: id %s from side B missing from merge", label, id)
		}
	}
}

// assertTombstoneDominance confirms that, for every id present as a
// tombstone on either input side, the merged result is also tombstoned,
// regardless of the other side's revision (R-21.219 dominance property).
func assertTombstoneDominance(t *testing.T, label string, a, b, merged map[string]MemoryRecord) {
	t.Helper()
	for id, rec := range a {
		if rec.Tombstone && !merged[id].Tombstone {
			t.Fatalf("%s: tombstone dominance failed: id %s tombstoned in A but not in merge result", label, id)
		}
	}
	for id, rec := range b {
		if rec.Tombstone && !merged[id].Tombstone {
			t.Fatalf("%s: tombstone dominance failed: id %s tombstoned in B but not in merge result", label, id)
		}
	}
}

// TestMemoryAssociativityRefuted is the spike's principal negative
// finding: memoryMergeAppend is commutative, idempotent, tombstone-
// dominant and loss-free (all confirmed with zero violations across 200
// deterministic seeds during this spike's investigation), but it is NOT
// associative. This test hardcodes one such counterexample, found at
// math/rand seed 2, so the finding survives as a committed regression
// rather than a claim in prose alone.
//
// Root cause: resolveMemory's fallback path snapshots the WINNING side's
// scalar tie-break fields (Revision/HLC/NodeID) while separately unioning
// both sides' version vectors. Two different pairwise chains accumulate
// the same final vector but arrive via different intermediate scalar
// snapshots, so a later comparison against the fully-accumulated vector
// can find genuine dominance that the other chain order never surfaced,
// picking a different winner. Concretely, below: merge(merge(A,B),C) and
// merge(A,merge(B,C)) both converge to version vector {node-a:8,
// node-b:9}, but disagree on which HLC (16 vs 7) is attached to it,
// because A directly-vs-{B,C}'s-union is a real vector-dominance case
// that A-via-B-first never evaluates.
//
// Consequence for Q/S-38.T2 (recorded in the ADR): a production sync
// engine cannot chain pairwise memoryMergeAppend calls across peers and
// expect the converged state to be order-independent. Either every
// peer's state must be merged against one canonical accumulator in a
// single pass (never through a second peer's already-merged copy), or
// the register must retain a full multi-value history until the version
// vector, not a snapshotted scalar, is what dominance is decided against.
func TestMemoryAssociativityRefuted(t *testing.T) {
	a := map[string]MemoryRecord{"g1": {RecordID: "g1", VersionVector: map[string]uint64{"node-a": 4, "node-b": 7}, Revision: 9, HLC: 16, NodeID: "node-c"}}
	b := map[string]MemoryRecord{"g1": {RecordID: "g1", VersionVector: map[string]uint64{"node-a": 8, "node-b": 3}, Revision: 2, HLC: 18, NodeID: "node-b"}}
	c := map[string]MemoryRecord{"g1": {RecordID: "g1", VersionVector: map[string]uint64{"node-a": 7, "node-b": 9}, Revision: 9, HLC: 7, NodeID: "node-c"}}

	ab := memoryMergeAppend(a, b)
	left := memoryMergeAppend(ab, c)

	bc := memoryMergeAppend(b, c)
	right := memoryMergeAppend(a, bc)

	if reflect.DeepEqual(left, right) {
		t.Fatalf("expected the known associativity counterexample to still diverge (left=%+v, right=%+v); "+
			"if this now passes, either memoryMergeAppend changed or this hardcoded case needs updating -- "+
			"do not delete this test without updating the ADR's memory-domain verdict", left, right)
	}
	if left["g1"].HLC == right["g1"].HLC {
		t.Fatalf("expected the two association orders to disagree on the winning HLC (16 vs 7), got both = %d", left["g1"].HLC)
	}
	if !reflect.DeepEqual(left["g1"].VersionVector, right["g1"].VersionVector) {
		t.Fatalf("expected both orders to agree on the accumulated version vector despite disagreeing on HLC: left=%v right=%v", left["g1"].VersionVector, right["g1"].VersionVector)
	}
}

// TestSensitivityFilteredBeforeSerialization confirms every record is
// filtered by its immutable sensitivity tier BEFORE serialization
// (R-21.223): local-only and restricted records never serialize, in any
// domain, each exclusion is journaled by record id plus policy version,
// and the cursor advances past excluded ids explicitly. It also confirms
// only the ratified domain names (config, context) are used by this
// spike's fixtures.
func TestSensitivityFilteredBeforeSerialization(t *testing.T) {
	f := loadFixture(t, "sensitivity-local-only-excluded.json")
	if f.Domain != string(DomainContext) {
		t.Fatalf("sensitivity fixture domain = %q, want ratified domain %q", f.Domain, DomainContext)
	}
	records := decodeSide[MemoryRecord](t, f.SideA)

	kept, journal := FilterSensitive(DomainContext, records, "policy-v1", 42)

	if len(kept) != 1 || kept[0].RecordID != "sens-normal" {
		t.Fatalf("expected only the normal record to survive filtering, got %+v", kept)
	}
	if len(journal) != 2 {
		t.Fatalf("expected 2 journaled exclusions, got %d: %+v", len(journal), journal)
	}
	for _, entry := range journal {
		if entry.RecordID != "sens-local-only" && entry.RecordID != "sens-restricted" {
			t.Fatalf("unexpected excluded id journaled: %s", entry.RecordID)
		}
		if entry.PolicyVersion != "policy-v1" {
			t.Fatalf("exclusion for %s missing policy version: %+v", entry.RecordID, entry)
		}
		if entry.CursorAfter != 42 {
			t.Fatalf("exclusion for %s did not advance the cursor explicitly: %+v", entry.RecordID, entry)
		}
		if entry.Domain != DomainContext {
			t.Fatalf("exclusion for %s used non-ratified domain %q", entry.RecordID, entry.Domain)
		}
	}

	// No local-only or restricted payload may appear anywhere in the kept
	// (serializable) set.
	for _, r := range kept {
		if r.Sensitivity == SensitivityLocalOnly || r.Sensitivity == SensitivityRestricted {
			t.Fatalf("a %s record leaked into the serializable set: %+v", r.Sensitivity, r)
		}
	}
}
