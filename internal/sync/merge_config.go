package sync

// Purpose (this file): the server-primary last-writer-wins merge, and the
//
//	journal entry it leaves behind whenever somebody's write loses.
//
// "LAST WRITER" MEANS LAST IN THE TOTAL ORDER, not last by any clock — see
//
//	ordering.go for why wall time is never compared.
//
// THE LOSING WRITE IS THE POINT OF THE JOURNAL. Server-primary means a
//
//	local edit made concurrently with a server one is discarded. That is
//	the correct outcome and it is still somebody's work disappearing, so
//	every discard is recorded with both revisions and both content hashes.
//	A merge that silently dropped them would be indistinguishable from one
//	that had a bug.
//
// DELETES ARE ORDINARY VALUES HERE. A config tombstone competes in the
//
//	total order like any other write; there is no dominant-tombstone rule,
//	unlike the memory domain. A configuration key that was deleted and then
//	re-set at a higher revision should be set.
//
// Inputs: two sides of one domain, keyed by record id.
// Outputs: the merged side, with every discard journaled.
// SPORT: internal/sync config merge (ADD) — P1-E17-W4-S38-T2.

// MergeServerPrimary merges local into server under the server-primary
// rule, journaling every local write the server outranks.
//
// The SERVER side is named, rather than two symmetric sides, because this
// strategy is not symmetric: `server` is the authority, and a caller that
// passed them the other way round would invert the rule silently. Two
// parameters called sideA and sideB would have allowed exactly that.
func MergeServerPrimary(
	journal *ConflictJournal, dc DomainClass, server, local map[string]Record,
) map[string]Record {
	merged := make(map[string]Record, len(server)+len(local))
	for id, rec := range server {
		merged[id] = rec
	}
	for id, loc := range local {
		srv, contested := server[id]
		if !contested {
			merged[id] = loc
			continue
		}
		merged[id] = resolveServerPrimary(journal, dc, srv, loc)
	}
	return merged
}

// resolveServerPrimary picks between one contested pair and journals the
// loser. Identical records are not a conflict and are not journaled: a
// journal full of entries where nothing was lost is one nobody reads.
func resolveServerPrimary(
	journal *ConflictJournal, dc DomainClass, server, local Record,
) Record {
	switch cmp := Compare(server.Order, local.Order); {
	case cmp > 0:
		journal.Record(conflictOf(dc, StrategyServerPrimaryLWW, server, local, ResolutionServerWon))
		return server
	case cmp < 0:
		journal.Record(conflictOf(dc, StrategyServerPrimaryLWW, local, server, ResolutionOrderWon))
		return local
	default:
		// Identical under the ordering. The server copy is kept so the
		// result is the server's bytes, but nothing was lost and nothing
		// is journaled.
		return server
	}
}

// conflictOf builds one journal entry from a resolved pair.
func conflictOf(dc DomainClass, strategy StrategyName, winner, loser Record, res Resolution) Conflict {
	return Conflict{
		Domain:     string(dc.Domain),
		Subkind:    dc.Subkind,
		Strategy:   strategy,
		RecordID:   winner.ID,
		Winner:     sideOf(winner),
		Loser:      sideOf(loser),
		Resolution: res,
	}
}

// sideOf renders one record as a journal side.
func sideOf(r Record) Side {
	return Side{NodeID: r.Order.NodeID, Revision: r.Order.Revision, HLC: r.Order.HLC, Hash: r.Hash}
}
