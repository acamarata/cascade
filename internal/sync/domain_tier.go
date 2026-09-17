package sync

import (
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
)

// Purpose (this file): which domains a peer at a given trust tier may sync
//
//	at all — the table, and the rule that anything not in it syncs nothing.
//
// A CLOSED TABLE, NOT A PREDICATE (R-21.224). Every (domain, tier) pair is
//
//	listed or it is no-sync. A predicate — "tier rank at least N" — would be
//	shorter and would answer for pairs nobody has thought about, which on
//	this particular question means replicating a domain to a class of
//	device nobody decided should have it. The table is committed as a
//	golden so a change to it is a visible diff rather than a changed
//	inequality.
//
// WHAT THE THREE TIERS GET, and why:
//
//   - CONTROLLER is the machine that holds everything anyway. All domains.
//   - WORKER-TRUSTED runs dispatched work, so it needs configuration, the
//     phase state that says what the work is, the blobs the work reads, and
//     the registry metadata that names providers. It does NOT get memory,
//     conversation or accounts: none of them is needed to run work, and
//     each is a standing copy of something personal on a machine whose
//     whole purpose is to be disposable.
//   - PAIRED-DEVICE syncs NOTHING in P1. Not because it could not, but
//     because nothing in P1 decides what a phone should hold, and the
//     answer to an undecided question about personal data is not "some".
//
// Inputs: a domain, a subkind and a peer's tier.
// Outputs: whether that peer may sync it.
// SPORT: internal/sync domain-tier eligibility (ADD) — P1-E17-W4-S38-T2.

// tierDomains is the closed table. A tier absent from this map, or a
// subkind absent from its set, syncs nothing.
var tierDomains = map[nodes.Tier]map[string]struct{}{
	nodes.TierController: {
		"config": {}, "registry": {}, "accounts": {}, "phase-state": {},
		"memory": {}, "conversation": {}, "blobs": {},
	},
	nodes.TierWorkerTrusted: {
		"config": {}, "phase-state": {}, "blobs": {}, "registry": {},
	},
	// Deliberately empty rather than absent: an empty set states that the
	// tier was considered and decided against, where an absent key would
	// read as one nobody got to.
	nodes.TierPairedDevice: {},
}

// EligibleForTier reports whether a peer at tier may sync domain/subkind.
//
// Fail-closed on every miss: an unregistered domain, an unlisted subkind,
// an unknown tier and the zero tier all return false. Each of those is a
// question nobody answered, and the safe answer to an unanswered question
// about where data goes is that it does not go.
func EligibleForTier(domain storage.DomainID, subkind string, tier nodes.Tier) bool {
	if _, ok := Lookup(domain, subkind); !ok {
		return false
	}
	if _, ok := nodes.Rank(tier); !ok {
		return false
	}
	allowed, known := tierDomains[tier]
	if !known {
		return false
	}
	_, permitted := allowed[subkind]
	return permitted
}

// domainTierTable renders the whole table in the golden's format: one line
// per (tier, subkind) pair that is permitted, sorted.
//
// Rendered from the SAME map EligibleForTier reads, so the golden cannot
// drift from the behaviour. A golden written by hand would record what
// somebody believed the table was.
func domainTierTable() string {
	var lines []string
	for tier, allowed := range tierDomains {
		if len(allowed) == 0 {
			lines = append(lines, string(tier)+"\t(no domains)")
			continue
		}
		for subkind := range allowed {
			lines = append(lines, string(tier)+"\t"+subkind)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}
