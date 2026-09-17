package sync

// Purpose (this file): version vectors, tombstone dominance, and the
//
//	refusal that keeps a stale peer from resurrecting deleted records.
//
// A TOMBSTONE DOMINATES, in either merge order (R-21.223). A delete that
//
//	lost to a concurrent update would come back, which for a record
//	somebody deleted on purpose is the worst outcome available: they asked
//	for it to be gone, it went, and then it reappeared. So the dominance
//	is unconditional and does not consult the total order.
//
// TOMBSTONES ARE NEVER PRUNED IN P1. `[sync].tombstone_retention = never`,
//
//	which means the "oldest retained tombstone" is the oldest tombstone
//	that ever existed and CursorTooOld cannot fire on a correctly-behaved
//	peer. The check is still here, and the retention value is still a
//	knob, because the day pruning becomes possible is the day a peer that
//	was offline across it would silently resurrect everything deleted in
//	the gap — and the refusal must already be the behaviour, not a change
//	made under pressure at that point.
//
// Inputs: two version vectors, or a peer's cursor.
// Outputs: dominance, a union, or a typed refusal.
// SPORT: internal/sync tombstones (ADD) — P1-E17-W4-S38-T2.

// VectorCompare reports whether a dominates b (1), b dominates a (-1), or
// the two are equal or CONCURRENT (0).
//
// Equal and concurrent share a return value on purpose: neither one lets
// the vector decide, and the caller falls back to the total order for both.
// Splitting them would invite a caller to treat "concurrent" as an error,
// which it is not — concurrent writes are the ordinary case this whole
// merge exists for.
func VectorCompare(a, b map[string]uint64) int {
	var aAhead, bAhead bool
	for k := range unionKeys(a, b) {
		switch av, bv := a[k], b[k]; {
		case av > bv:
			aAhead = true
		case bv > av:
			bAhead = true
		}
	}
	switch {
	case aAhead && !bAhead:
		return 1
	case bAhead && !aAhead:
		return -1
	default:
		return 0
	}
}

// VectorUnion returns the component-wise maximum, which is the vector a
// causally correct merge carries forward.
//
// Carrying the union rather than the winner's own vector is what makes the
// merge associative: a peer that later compares against this result must
// see that it already knows everything BOTH sides knew, or a third merge
// would reopen a conflict the first two settled.
//
// ZERO COUNTERS ARE DROPPED, and that is not tidying. A counter of zero
// says "this node has made no writes I know of", which is exactly what an
// ABSENT key says — VectorCompare reads a missing key as zero. Keeping
// them made the union non-commutative: the obvious implementation copies
// the first vector wholesale and then takes `v > out[k]` from the second,
// so a zero from the second side was dropped while a zero from the first
// was kept, and union(A, B) and union(B, A) came out with different key
// sets for the same meaning. A property test over generated inputs found
// it; no hand-written case had two vectors where one carried a zero the
// other lacked, because nobody writes that down on purpose.
func VectorUnion(a, b map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(a)+len(b))
	for _, side := range []map[string]uint64{a, b} {
		for k, v := range side {
			if v == 0 {
				continue
			}
			if v > out[k] {
				out[k] = v
			}
		}
	}
	return out
}

// unionKeys returns every key present in either vector.
func unionKeys(a, b map[string]uint64) map[string]struct{} {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}

// OldestRetainedTombstone is the lowest revision whose tombstone this
// device still holds.
//
// It is ZERO, and says so plainly rather than hiding behind a lookup:
// tombstoneRetention is the compile-time constant `never` (config.go), so
// nothing is ever pruned and every tombstone ever written is still here.
// Every peer cursor is therefore catchable today, and CheckPeerCursor
// below cannot currently refuse.
//
// It exists as a function anyway, and the guard is wired, because the day
// retention becomes settable is the day the resurrection hazard becomes
// real — and the cost of finding that out then is a fleet quietly handing
// deleted records back to each other. Wiring the check now costs one
// comparison; discovering it later costs the data.
// A second retention policy must change this function as well as the
// constant; tombstone_test.go pins the pair together so it cannot be
// added while this still answers zero.
func OldestRetainedTombstone() uint64 {
	return 0
}

// CheckPeerCursor refuses a peer whose cursor predates the oldest retained
// tombstone.
//
// Such a peer cannot be brought up to date incrementally: the deletes it
// missed are the ones that were pruned, so an incremental catch-up would
// hand it back every record it should have removed. A full resync is the
// only correct answer, and saying so is better than performing a
// resurrection nobody would notice until the records reappeared.
func CheckPeerCursor(peerCursorRevision, oldestRetainedTombstone uint64) error {
	if peerCursorRevision < oldestRetainedTombstone {
		return ErrCursorTooOldf(peerCursorRevision, oldestRetainedTombstone)
	}
	return nil
}
