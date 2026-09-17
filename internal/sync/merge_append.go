package sync

// Purpose (this file): the append-merge for the record domains — memory
//
//	and conversation — where neither side is an authority and neither
//	side's records may be lost.
//
// NO SILENT LOSS IS THE WHOLE CONTRACT. Every record id present on either
//
//	side is present in the result. The only thing a merge decides is which
//	COPY of a contested id survives, never whether the id survives, so a
//	record written on a laptop that was offline for a week is still there
//	after the merge.
//
// THE ORDER OF THE THREE RULES IS LOAD-BEARING:
//
//  1. a tombstone dominates, unconditionally — a delete that lost to a
//     concurrent update would resurrect a record somebody removed;
//  2. otherwise version-vector dominance decides, because that is the
//     causal answer and it is the one that is actually true;
//  3. only for genuinely concurrent writes does the total order decide,
//     and its job there is to be DETERMINISTIC rather than right — two
//     peers merging the same pair must reach the same answer.
//
// COMMUTATIVE, ASSOCIATIVE, IDEMPOTENT by construction: the per-id
//
//	resolution is a total selection over a pair, and the merged record
//	carries the UNION of both vectors so a third merge sees a record that
//	already knows what both sides knew.
//
// Inputs: two sides of one record domain.
// Outputs: the merged side, with dominated updates journaled.
// SPORT: internal/sync append merge (ADD) — P1-E17-W4-S38-T2.

// MergeAppend unions two sides of a record domain.
//
// The sides are symmetric, unlike the server-primary merge: there is no
// authority here, and a caller may pass them in either order.
func MergeAppend(journal *ConflictJournal, dc DomainClass, sideA, sideB map[string]Record) map[string]Record {
	merged := make(map[string]Record, len(sideA)+len(sideB))
	for id, rec := range sideA {
		merged[id] = rec
	}
	for id, rec := range sideB {
		existing, contested := merged[id]
		if !contested {
			merged[id] = rec
			continue
		}
		merged[id] = resolveAppend(journal, dc, existing, rec)
	}
	return merged
}

// resolveAppend picks between two copies of one record id.
func resolveAppend(journal *ConflictJournal, dc DomainClass, a, b Record) Record {
	winner, loser, res := selectAppend(a, b)
	if res != "" {
		journal.Record(conflictOf(dc, StrategyAppendTombstone, winner, loser, res))
	}
	winner.Vector = VectorUnion(a.Vector, b.Vector)
	return winner
}

// selectAppend applies the three rules in order and reports which fired.
//
// An empty Resolution means nothing was lost: the two copies agree, or one
// causally supersedes the other, which is a merge doing its job rather
// than a conflict. Journaling those would bury the entries that matter.
func selectAppend(a, b Record) (winner, loser Record, res Resolution) {
	if a.Tombstone != b.Tombstone {
		if a.Tombstone {
			return a, b, ResolutionTombstoneWon
		}
		return b, a, ResolutionTombstoneWon
	}
	switch VectorCompare(a.Vector, b.Vector) {
	case 1:
		return a, b, ""
	case -1:
		return b, a, ""
	}
	// Genuinely concurrent, or identical. The total order decides, and
	// only a real difference is journaled.
	switch cmp := Compare(a.Order, b.Order); {
	case cmp > 0:
		return a, b, ResolutionOrderWon
	case cmp < 0:
		return b, a, ResolutionOrderWon
	default:
		return a, b, ""
	}
}

// normaliseAppend is MergeAppend's idempotence target: what merging a side
// with itself must produce.
//
// Stated as its own function rather than compared against the input
// literal, because the merge canonicalises the version vector and the
// input may not be canonical. A property test comparing merge(A, A) to A
// would then fail for a reason that is not a bug, and the usual response
// to that is to weaken the property.
func normaliseAppend(side map[string]Record) map[string]Record {
	out := make(map[string]Record, len(side))
	for id, rec := range side {
		cp := rec
		cp.Vector = VectorUnion(rec.Vector, nil)
		out[id] = cp
	}
	return out
}
