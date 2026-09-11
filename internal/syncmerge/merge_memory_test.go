//go:build spike

package syncmerge

import (
	"errors"
	"reflect"
	"testing"
)

// TestMemoryMergeAppend exercises memoryMergeAppend against every
// memory-domain adversarial fixture: concurrent update, double tombstone,
// resurrection suppression, and the stale-peer ErrCursorTooOld path.
func TestMemoryMergeAppend(t *testing.T) {
	t.Run("concurrent-update", testMemoryConcurrentUpdate)
	t.Run("double-tombstone", testMemoryDoubleTombstone)
	t.Run("resurrection-suppressed", testMemoryResurrectionSuppressed)
	t.Run("stale-peer-cursor-too-old", testMemoryStalePeerCursor)
}

func testMemoryConcurrentUpdate(t *testing.T) {
	f := loadFixture(t, "memory-concurrent-update.json")
	a := toMap(decodeSide[MemoryRecord](t, f.SideA))
	b := toMap(decodeSide[MemoryRecord](t, f.SideB))

	merged := memoryMergeAppend(a, b)
	reversed := memoryMergeAppend(b, a)
	for id := range merged {
		if !reflect.DeepEqual(merged[id], reversed[id]) {
			t.Fatalf("concurrent update merge is not commutative for %s: %+v vs %+v", id, merged[id], reversed[id])
		}
	}
}

func testMemoryDoubleTombstone(t *testing.T) {
	f := loadFixture(t, "memory-double-tombstone.json")
	a := toMap(decodeSide[MemoryRecord](t, f.SideA))
	b := toMap(decodeSide[MemoryRecord](t, f.SideB))

	merged := memoryMergeAppend(a, b)
	self := memoryMergeAppend(a, a)
	for id, rec := range merged {
		if !rec.Tombstone {
			t.Fatalf("double-tombstone: id %s not tombstoned in merge result", id)
		}
		if !self[id].Tombstone {
			t.Fatalf("double-tombstone: merge(A,A) lost tombstone for %s (idempotence)", id)
		}
	}
}

func testMemoryResurrectionSuppressed(t *testing.T) {
	f := loadFixture(t, "memory-resurrection-suppressed.json")
	a := toMap(decodeSide[MemoryRecord](t, f.SideA))
	b := toMap(decodeSide[MemoryRecord](t, f.SideB))

	tombstoneID := findTombstoneID(a, b)
	if tombstoneID == "" {
		t.Fatalf("fixture setup error: no tombstoned record in either side")
	}
	merged := memoryMergeAppend(a, b)
	if !merged[tombstoneID].Tombstone {
		t.Fatalf("resurrection NOT suppressed: a concurrent update revived tombstoned record %s", tombstoneID)
	}
	reversed := memoryMergeAppend(b, a)
	if !reversed[tombstoneID].Tombstone {
		t.Fatalf("resurrection NOT suppressed in reverse order for %s", tombstoneID)
	}
}

func findTombstoneID(sides ...map[string]MemoryRecord) string {
	for _, side := range sides {
		for id, r := range side {
			if r.Tombstone {
				return id
			}
		}
	}
	return ""
}

func testMemoryStalePeerCursor(t *testing.T) {
	f := loadFixture(t, "memory-stale-peer-cursor-too-old.json")
	a := decodeSide[MemoryRecord](t, f.SideA)
	b := decodeSide[MemoryRecord](t, f.SideB)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("fixture must carry exactly one record per side (peerCursor, oldestRetained), got %d/%d", len(a), len(b))
	}
	peerCursor := a[0].Revision
	oldestRetained := b[0].Revision
	if peerCursor >= oldestRetained {
		t.Fatalf("fixture setup error: peer cursor (%d) must predate oldest retained tombstone (%d)", peerCursor, oldestRetained)
	}
	err := checkPeerCursor(peerCursor, oldestRetained)
	if !errors.Is(err, ErrPeerCursorTooOld) {
		t.Fatalf("checkPeerCursor(%d,%d) = %v, want ErrPeerCursorTooOld", peerCursor, oldestRetained, err)
	}
}

// TestMemoryTombstoneNeverPruned confirms a tombstone dominates a
// concurrent update regardless of which side of the merge call carries the
// higher revision (R-21.223 domain 2 dominance property, checked directly
// rather than only through a fixture).
func TestMemoryTombstoneNeverPruned(t *testing.T) {
	tomb := MemoryRecord{RecordID: "m", Tombstone: true, Revision: 1, HLC: 1, NodeID: "a", VersionVector: map[string]uint64{"a": 1}}
	update := MemoryRecord{RecordID: "m", Tombstone: false, Revision: 99, HLC: 99, NodeID: "b", VersionVector: map[string]uint64{"b": 99}}

	if got := resolveMemory(tomb, update); !got.Tombstone {
		t.Fatalf("resolveMemory(tomb, higher-revision update) = %+v, want tombstone to dominate", got)
	}
	if got := resolveMemory(update, tomb); !got.Tombstone {
		t.Fatalf("resolveMemory(update, tomb) = %+v, want tombstone to dominate in either order", got)
	}
}
