package sync

import "github.com/acamarata/cascade/internal/hooks/egress"

// Purpose (this file): the metadata-only server-primary merge for the
//
//	registry and accounts domains — and the structural reason no credential
//	can ride it.
//
// METADATA-ONLY IS ENFORCED, NOT DOCUMENTED. The merge takes Records, and
//
//	a Record has no field a secret fits in: the payload it carries is the
//	metadata, and the credential lives in the vault under a key name. That
//	is the enforcement — there is nothing to leak rather than a rule
//	against leaking — and MergeMetadataOnly additionally REFUSES any record
//	whose resolved tier is restricted or local-only, so a record that
//	somebody re-tiered on the way in is dropped and journaled rather than
//	replicated.
//
// WHY A SEPARATE FUNCTION FROM MergeServerPrimary, given the same
//
//	ordering: because the refusal above is the difference, and folding it
//	into the general merge as a flag would put a credential-safety rule
//	behind a boolean somebody could pass wrong. Two call sites, one of
//	which cannot ship a secret, is worth more than one call site with an
//	option.
//
// Inputs: the server and local sides of a metadata domain.
// Outputs: the merged side, with refusals journaled.
// SPORT: internal/sync metadata merge (ADD) — P1-E17-W4-S38-T2.

// MergeMetadataOnly merges the server and local sides of a metadata domain
// under the server-primary rule, dropping any record whose own sensitivity
// tier says it must not leave this device.
//
// The drop is journaled as a refusal so an operator can see that a record
// exists and did not replicate. Silently omitting it would look identical
// to the record never having been written.
func MergeMetadataOnly(
	journal *ConflictJournal, dc DomainClass, server, local map[string]Record,
) map[string]Record {
	merged := MergeServerPrimary(journal, dc, admissible(journal, dc, server), admissible(journal, dc, local))
	return merged
}

// admissible drops every record whose resolved tier forbids replication,
// journaling each one.
//
// This runs on BOTH sides, not only the local one. A restricted record
// arriving from the server is just as much a leak as one leaving: it means
// something upstream serialized what it should not have, and quietly
// accepting it would spread the fault rather than surface it.
func admissible(journal *ConflictJournal, dc DomainClass, side map[string]Record) map[string]Record {
	out := make(map[string]Record, len(side))
	for id, rec := range side {
		switch rec.Tier.Resolve() {
		case egress.TierPublic, egress.TierInternal:
			out[id] = rec
		case egress.TierUnset:
			// Unreachable: Resolve never returns unset. Named so a change
			// to the tier set fails to compile here rather than falling
			// through to the permissive branch.
			out[id] = rec
		case egress.TierLocalOnly, egress.TierRestricted:
			journal.Record(Conflict{
				Domain: string(dc.Domain), Subkind: dc.Subkind,
				Strategy: StrategyMetadataOnly, RecordID: id,
				Loser:      sideOf(rec),
				Resolution: ResolutionRefused,
				Detail: "the record's own sensitivity tier forbids replication; " +
					"it stays on the device that holds it",
			})
		}
	}
	return out
}
