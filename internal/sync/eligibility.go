package sync

import (
	"sort"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
)

// Purpose (this file): the single question a caller asks before any record
//
//	is serialized for a peer — may THIS record go to THAT peer?
//
// THREE GATES, AND ALL THREE MUST PASS. The domain must be registered and
//
//	synced at all; the peer's trust tier must be permitted that domain
//	(domain_tier.go); and the record's OWN sensitivity tier must allow it
//	to leave. They are separate because they answer different questions and
//	any one of them can say no on its own — a config record marked
//	local-only stays put even on a controller, and a perfectly ordinary
//	memory record does not go to a worker.
//
// NO TIER IS EVER WIDENED HERE (06 §5.16). This file only ever narrows:
//
//	there is no branch that promotes a record, relaxes a tier, or treats a
//	missing value as permissive. A caller looking for the place to add "but
//	allow it when X" will not find one, which is deliberate.
//
// Inputs: a record and the peer's trust tier.
// Outputs: whether it may be sent, and why not when it may not.
// SPORT: internal/sync eligibility (ADD) — P1-E17-W4-S38-T2.

// EligibilityVerdict is the answer, with the reason a caller journals.
type EligibilityVerdict struct {
	// Eligible is whether the record may be serialized for this peer.
	Eligible bool
	// Reason names the gate that refused. Empty when eligible.
	Reason string
}

// Eligible reports whether rec may be sent to a peer at tier.
//
// The gates run most-fundamental-first, so the reason a caller journals is
// the one an operator would act on: "this domain does not sync" is more
// useful than "this peer's tier is too low" when both are true.
func Eligible(rec Record, tier nodes.Tier) EligibilityVerdict {
	dc, ok := Lookup(rec.Domain, rec.Subkind)
	if !ok {
		return EligibilityVerdict{Reason: "domain-unregistered"}
	}
	if dc.Class == ClassLocalOnly {
		return EligibilityVerdict{Reason: "domain-local-only"}
	}
	if !EligibleForTier(rec.Domain, rec.Subkind, tier) {
		return EligibilityVerdict{Reason: "tier-not-permitted"}
	}
	switch rec.Tier.Resolve() {
	case egress.TierPublic, egress.TierInternal, egress.TierUnset:
		// Nothing further to check. TierUnset is unreachable — Resolve
		// never returns it — and is named so a change to the tier set
		// fails to compile here rather than reaching the default.
	case egress.TierLocalOnly:
		return EligibilityVerdict{Reason: "sensitivity-local-only"}
	case egress.TierRestricted:
		// Restricted needs worker-trusted or better. The gate is asked
		// through nodes.Satisfies rather than compared here, so the rank
		// ordering lives in exactly one place (R-21.220).
		if !nodes.Satisfies(tier, nodes.GateRestricted) {
			return EligibilityVerdict{Reason: "sensitivity-restricted"}
		}
	}
	return EligibilityVerdict{Eligible: true}
}

// EligibleDomains lists the subkinds a peer at tier may sync, sorted, for
// diagnostics and for `sync status`.
func EligibleDomains(tier nodes.Tier) []string {
	var out []string
	for _, dc := range AllCoreClasses() {
		if EligibleForTier(dc.Domain, dc.Subkind, tier) {
			out = append(out, dc.Subkind)
		}
	}
	sort.Strings(out)
	return out
}
