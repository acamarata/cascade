//go:build spike

package syncmerge

// MemoryRecord is one memory/conversation record (R-21.223 domain 2).
// Records carry a globally unique immutable RecordID and a VersionVector
// (node id -> counter). Concurrent updates to the same id resolve by
// version-vector dominance, then by the same revision/HLC/node-id ordering
// used for config. A Tombstone for a RecordID dominates every concurrent
// update to it, in either merge order, and is never pruned in P1.
type MemoryRecord struct {
	RecordID      string
	VersionVector map[string]uint64
	Revision      uint64
	HLC           uint64
	NodeID        string
	Content       string
	Sensitivity   Sensitivity
	Tombstone     bool
}

func (r MemoryRecord) recordID() string             { return r.RecordID }
func (r MemoryRecord) sensitivityTier() Sensitivity { return r.Sensitivity }

// vvCompare reports whether vector a dominates b (every counter in a is >=
// the corresponding counter in b, and at least one is strictly greater),
// whether they are equal, or whether they are concurrent (neither
// dominates). It returns 1 (a dominates), -1 (b dominates), or 0
// (equal or concurrent -- the caller falls back to the total order for
// concurrent updates).
func vvCompare(a, b map[string]uint64) int {
	aGreater, bGreater := false, false
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range keys {
		av, bv := a[k], b[k]
		if av > bv {
			aGreater = true
		} else if bv > av {
			bGreater = true
		}
	}
	switch {
	case aGreater && !bGreater:
		return 1
	case bGreater && !aGreater:
		return -1
	default:
		return 0
	}
}

// mvUnion returns the component-wise max of two version vectors, which is
// the vector a causally-correct merge must carry forward.
func vvUnion(a, b map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

// resolveMemory picks the winner between two concurrent MemoryRecord copies
// of the same id. A Tombstone always outranks a non-tombstone, regardless
// of revision (dominance property); between two tombstones or two
// non-tombstones, version-vector dominance decides, falling back to the
// revision/HLC/node-id total order (via compareConfig's ordering, reused
// here on the shared three fields) for genuinely concurrent writes.
func resolveMemory(a, b MemoryRecord) MemoryRecord {
	if a.Tombstone != b.Tombstone {
		if a.Tombstone {
			return a
		}
		return b
	}
	winner := a
	switch vvCompare(a.VersionVector, b.VersionVector) {
	case 1:
		winner = a
	case -1:
		winner = b
	default:
		cfgA := ConfigRecord{Revision: a.Revision, HLC: a.HLC, NodeID: a.NodeID}
		cfgB := ConfigRecord{Revision: b.Revision, HLC: b.HLC, NodeID: b.NodeID}
		if compareConfig(cfgA, cfgB) >= 0 {
			winner = a
		} else {
			winner = b
		}
	}
	winner.VersionVector = vvUnion(a.VersionVector, b.VersionVector)
	return winner
}

// memoryMergeAppend merges two sides of the memory domain by union of
// record ids, resolving any id present on both sides via resolveMemory. It
// is commutative, associative and idempotent by construction: resolveMemory
// is a total, order-independent selection over each pair.
func memoryMergeAppend(sideA, sideB map[string]MemoryRecord) map[string]MemoryRecord {
	merged := make(map[string]MemoryRecord, len(sideA)+len(sideB))
	for id, rec := range sideA {
		merged[id] = rec
	}
	for id, rec := range sideB {
		if existing, ok := merged[id]; ok {
			merged[id] = resolveMemory(existing, rec)
		} else {
			merged[id] = rec
		}
	}
	return merged
}

// normaliseMemory is the idempotence target for memoryMergeAppend, matching
// normaliseConfig's role for the config domain.
func normaliseMemory(side map[string]MemoryRecord) map[string]MemoryRecord {
	out := make(map[string]MemoryRecord, len(side))
	for id, rec := range side {
		cp := rec
		cp.VersionVector = vvUnion(rec.VersionVector, nil)
		out[id] = cp
	}
	return out
}

// checkPeerCursor refuses a peer resync whose cursor predates the oldest
// retained tombstone revision, per R-21.223's never-pruned tombstone rule.
// Tombstones are never pruned in P1 ([sync].tombstone_retention = never),
// so a stale cursor cannot be satisfied incrementally without resurrecting
// deleted records; the caller must perform a full resync instead.
func checkPeerCursor(peerCursorRevision, oldestRetainedTombstoneRevision uint64) error {
	if peerCursorRevision < oldestRetainedTombstoneRevision {
		return ErrPeerCursorTooOld
	}
	return nil
}
