package nodes

import "github.com/acamarata/cascade/pkg/provider"

// Purpose: the trust-tier half of the placement decision — whether a node's
//
//	tier may run work of a given sensitivity at all.
//
// Inputs: a work sensitivity and an enrolled node's trust tier.
// Outputs: an exclusion reason, or nothing when the tier clears.
// Constraints: fail-closed in both directions. An unresolvable sensitivity
//
//	resolves to the most restrictive value, and an unrecognized tier clears
//	nothing. Neither is ever treated as permissive. The work sensitivity is
//	provider.SensitivityTier (contract:sensitivity-tier); this package keeps
//	only the WIRE codec for its legacy three-word vocabulary, whose bytes
//	(json:"sensitivity" on DispatchRequest) are unchanged.
//
// SPORT: internal/nodes placement trust filter (ADD) — P1-E17-W4-S37-T1.

// Wire spellings of the node.dispatch sensitivity field. "normal" is this
// wire's single word for work with no tier rule of its own.
const (
	wireLocalOnly  = "local-only"
	wireRestricted = "restricted"
	wireNormal     = "normal"
)

// decodeWireSensitivity maps a node.dispatch wire sensitivity to its tier:
// "local-only" and "restricted" to themselves, "normal" to
// SensitivityInternal, and anything else - including the empty string and
// every other spelling - to SensitivityLocalOnly, so work whose
// classification could not be read never leaves the controller machine.
func decodeWireSensitivity(raw string) provider.SensitivityTier {
	switch raw {
	case wireLocalOnly:
		return provider.SensitivityLocalOnly
	case wireRestricted:
		return provider.SensitivityRestricted
	case wireNormal:
		return provider.SensitivityInternal
	default:
		return provider.SensitivityLocalOnly
	}
}

// encodeWireSensitivity is decodeWireSensitivity's inverse for the wire:
// SensitivityInternal and SensitivityPublic both encode as "normal" (the
// wire has no finer word), restricted as "restricted", and local-only or
// any out-of-range value as "local-only".
func encodeWireSensitivity(t provider.SensitivityTier) string {
	switch t {
	case provider.SensitivityRestricted:
		return wireRestricted
	case provider.SensitivityInternal, provider.SensitivityPublic:
		return wireNormal
	case provider.SensitivityLocalOnly:
		return wireLocalOnly
	default:
		return wireLocalOnly
	}
}

// excludedByTrust reports whether tier may run work of this sensitivity.
//
// # Why local-only is not Satisfies(tier, GateLocalOnly)
//
// trust.go's GateLocalOnly is an identity check against TierController,
// which is the right rule for a gate asking "is this machine the
// controller". It is the WRONG rule here: every Candidate is an ENROLLED
// node, and an enrolled record is by definition not the controller machine
// running this engine — whatever tier string the record happens to carry.
// Routing local-only work to a remote node because its stored tier read
// "controller" is exactly the leak the classification exists to prevent, so
// local-only work excludes every candidate unconditionally.
func excludedByTrust(sensitivity provider.SensitivityTier, tier Tier) (reason ExclusionReason, detail string, excluded bool) {
	switch sensitivity {
	case provider.SensitivityLocalOnly:
		return ReasonLocalOnlyWork, "work is local-only: the controller machine is the only place it may run", true
	case provider.SensitivityRestricted:
		if !Satisfies(tier, GateRestricted) {
			return ReasonTierTooLow, "trust_tier " + tierName(tier) + " is below worker-trusted", true
		}
		return "", "", false
	case provider.SensitivityInternal, provider.SensitivityPublic:
		// Internal and public work impose no tier rule of their own, but
		// an unrecognized tier still clears nothing: Rank fails closed, and
		// a record carrying a tier this build does not know is a record
		// whose authorization cannot be reasoned about.
		if _, ok := Rank(tier); !ok {
			return ReasonTierTooLow, "trust_tier " + tierName(tier) + " is not a recognized tier", true
		}
		return "", "", false
	default:
		// A value above SensitivityPublic is not a tier. It is excluded
		// exactly like local-only work: a classification this build cannot
		// read is the case where guessing wrong leaks work off the box.
		return ReasonLocalOnlyWork, "work sensitivity " + sensitivity.String() + " is not a tier: it stays on the controller machine", true
	}
}

// tierName renders a tier for an error detail, naming the empty value
// explicitly rather than rendering it as an empty string in the middle of
// a sentence.
func tierName(t Tier) string {
	if t == "" {
		return "(unset)"
	}
	return string(t)
}
