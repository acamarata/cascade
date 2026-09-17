package sync

// Purpose (this file): the append merge — tombstone dominance, version
//   vectors, the properties, and the no-silent-loss rule.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// memoryDomain is the registered memory class these tests merge.
func memoryDomain(t *testing.T) DomainClass {
	t.Helper()
	dc, ok := Lookup(storage.DomainMemory, "memory")
	if !ok {
		t.Fatal("the memory domain is not registered; these tests would prove nothing")
	}
	return dc
}

// mrec builds one memory record.
func mrec(id string, vector map[string]uint64, revision uint64, node, hash string) Record {
	return Record{
		Domain: storage.DomainMemory, Subkind: "memory", ID: id, Hash: hash,
		Vector: vector,
		Order:  OrderKey{Revision: revision, NodeID: node},
	}
}

// TestTombstoneDominance is the rule a delete must never lose. A record
// somebody removed coming back is the worst outcome this merge can produce.
func TestTombstoneDominance(t *testing.T) {
	dc := memoryDomain(t)
	deleted := mrec("r1", map[string]uint64{"a": 1}, 1, "a", "")
	deleted.Tombstone = true
	// The update is NEWER by every other measure: higher vector, higher
	// revision. It must still lose.
	updated := mrec("r1", map[string]uint64{"a": 1, "b": 9}, 99, "b", "content")

	for _, order := range []struct {
		name string
		x, y Record
	}{
		{"tombstone first", deleted, updated},
		{"tombstone second", updated, deleted},
	} {
		t.Run(order.name, func(t *testing.T) {
			journal := &ConflictJournal{}
			merged := MergeAppend(journal, dc,
				map[string]Record{"r1": order.x}, map[string]Record{"r1": order.y})
			if !merged["r1"].Tombstone {
				t.Fatal("a concurrent update resurrected a deleted record")
			}
			if journal.Len() != 1 || journal.List()[0].Resolution != ResolutionTombstoneWon {
				t.Errorf("the dominated update was not journaled as such: %+v", journal.List())
			}
		})
	}
}

// TestVersionVectorDominanceBeatsTheTotalOrder proves the causal answer is
// preferred. A record that causally supersedes another must win even when
// the total order would say otherwise — and it is not a conflict.
func TestVersionVectorDominanceBeatsTheTotalOrder(t *testing.T) {
	dc := memoryDomain(t)
	// `later` dominates causally but has the LOWER revision.
	later := mrec("r1", map[string]uint64{"a": 5, "b": 3}, 1, "a", "causally-later")
	earlier := mrec("r1", map[string]uint64{"a": 4, "b": 3}, 99, "z", "causally-earlier")

	journal := &ConflictJournal{}
	merged := MergeAppend(journal, dc,
		map[string]Record{"r1": earlier}, map[string]Record{"r1": later})
	if merged["r1"].Hash != "causally-later" {
		t.Fatalf("merged = %q, want the causally later record", merged["r1"].Hash)
	}
	if journal.Len() != 0 {
		t.Errorf("a causal supersession was journaled as a conflict: %+v", journal.List())
	}
}

// TestMergedRecordCarriesTheVectorUnion is what makes the merge
// associative: a third peer must see that this result already knows what
// both sides knew, or it would reopen a settled conflict.
func TestMergedRecordCarriesTheVectorUnion(t *testing.T) {
	dc := memoryDomain(t)
	a := mrec("r1", map[string]uint64{"a": 3}, 1, "a", "ha")
	b := mrec("r1", map[string]uint64{"b": 7}, 2, "b", "hb")

	merged := MergeAppend(&ConflictJournal{}, dc,
		map[string]Record{"r1": a}, map[string]Record{"r1": b})
	got := merged["r1"].Vector
	if got["a"] != 3 || got["b"] != 7 {
		t.Fatalf("merged vector = %v, want the component-wise union", got)
	}
}

// TestSyncMergeConvergence is the property suite the ticket names: the
// append merge must be commutative, associative and idempotent, and must
// never lose a record id.
func TestSyncMergeConvergence(t *testing.T) {
	dc := memoryDomain(t)
	a := map[string]Record{
		"shared": mrec("shared", map[string]uint64{"a": 2}, 5, "a", "ha"),
		"only-a": mrec("only-a", map[string]uint64{"a": 1}, 1, "a", "oa"),
	}
	b := map[string]Record{
		"shared": mrec("shared", map[string]uint64{"b": 2}, 5, "b", "hb"),
		"only-b": mrec("only-b", map[string]uint64{"b": 1}, 1, "b", "ob"),
	}
	c := map[string]Record{
		"shared": mrec("shared", map[string]uint64{"c": 9}, 9, "c", "hc"),
		"only-c": mrec("only-c", map[string]uint64{"c": 1}, 1, "c", "oc"),
	}

	t.Run("commutative", func(t *testing.T) {
		ab := MergeAppend(&ConflictJournal{}, dc, a, b)
		ba := MergeAppend(&ConflictJournal{}, dc, b, a)
		assertSameState(t, ab, ba)
	})
	t.Run("associative", func(t *testing.T) {
		left := MergeAppend(&ConflictJournal{}, dc, MergeAppend(&ConflictJournal{}, dc, a, b), c)
		right := MergeAppend(&ConflictJournal{}, dc, a, MergeAppend(&ConflictJournal{}, dc, b, c))
		assertSameState(t, left, right)
	})
	t.Run("idempotent", func(t *testing.T) {
		assertSameState(t, MergeAppend(&ConflictJournal{}, dc, a, a), normaliseAppend(a))
	})
	t.Run("no silent loss", func(t *testing.T) {
		merged := MergeAppend(&ConflictJournal{}, dc, MergeAppend(&ConflictJournal{}, dc, a, b), c)
		for _, id := range []string{"shared", "only-a", "only-b", "only-c"} {
			if _, ok := merged[id]; !ok {
				t.Errorf("record %q vanished in the merge", id)
			}
		}
	})
}

// assertSameState compares two merged states by id, hash and vector.
func assertSameState(t *testing.T, got, want map[string]Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d records vs %d", len(got), len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("record %q missing", id)
			continue
		}
		if g.Hash != w.Hash {
			t.Errorf("record %q: hash %q vs %q", id, g.Hash, w.Hash)
		}
		if len(g.Vector) != len(w.Vector) {
			t.Errorf("record %q: vector %v vs %v", id, g.Vector, w.Vector)
			continue
		}
		for k, v := range w.Vector {
			if g.Vector[k] != v {
				t.Errorf("record %q: vector %v vs %v", id, g.Vector, w.Vector)
				break
			}
		}
	}
}

// TestPartitionRejoinConvergence walks the real scenario: two peers write
// independently while partitioned, one deletes a record the other edits,
// and both converge on rejoin.
func TestPartitionRejoinConvergence(t *testing.T) {
	dc := memoryDomain(t)
	base := mrec("r1", map[string]uint64{"a": 1}, 1, "a", "base")

	// Peer A edits. Peer B deletes.
	edited := mrec("r1", map[string]uint64{"a": 2}, 2, "a", "edited")
	deleted := mrec("r1", map[string]uint64{"a": 1, "b": 1}, 2, "b", "")
	deleted.Tombstone = true
	sideA := map[string]Record{"r1": edited, "a-only": base}
	sideB := map[string]Record{"r1": deleted, "b-only": base}

	ab := MergeAppend(&ConflictJournal{}, dc, sideA, sideB)
	ba := MergeAppend(&ConflictJournal{}, dc, sideB, sideA)
	assertSameState(t, ab, ba)
	if !ab["r1"].Tombstone {
		t.Error("the delete lost on rejoin")
	}
	for _, id := range []string{"a-only", "b-only"} {
		if _, ok := ab[id]; !ok {
			t.Errorf("%q was lost across the partition", id)
		}
	}
}
