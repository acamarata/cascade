package nodes

// Purpose: the trust-tier half of the placement decision — whether a node's
//
//	tier may run work of a given sensitivity at all.
//
// Inputs: a work sensitivity and an enrolled node's trust tier.
// Outputs: an exclusion reason, or nothing when the tier clears.
// Constraints: fail-closed in both directions. An unresolvable sensitivity
//
//	resolves to the most restrictive value, and an unrecognized tier clears
//	nothing. Neither is ever treated as permissive.
//
// SPORT: internal/nodes placement trust filter (ADD) — P1-E17-W4-S37-T1.

// Sensitivity is the placement-relevant classification of a unit of work.
//
// The zero value is deliberately NOT "unrestricted": an empty or
// unrecognized sensitivity resolves to SensitivityLocalOnly, the most
// restrictive value, so work whose classification could not be determined
// never leaves the controller machine by default.
type Sensitivity string

const (
	// SensitivityLocalOnly is work that must run on the controller machine
	// itself. It is never eligible on any enrolled node.
	SensitivityLocalOnly Sensitivity = "local-only"
	// SensitivityRestricted is work that may run on a node whose tier is at
	// least worker-trusted.
	SensitivityRestricted Sensitivity = "restricted"
	// SensitivityNormal is work with no tier restriction of its own; every
	// other placement filter still applies.
	SensitivityNormal Sensitivity = "normal"
)

// ResolveSensitivity maps a raw classification string to a Sensitivity,
// resolving anything it does not recognize — including the empty string —
// to SensitivityLocalOnly.
//
// This is the fail-closed rule stated as code rather than left to each
// caller: a classification that could not be resolved is the case where
// guessing wrong leaks work off the controller machine, so it resolves the
// way that cannot.
func ResolveSensitivity(raw string) Sensitivity {
	switch Sensitivity(raw) {
	case SensitivityLocalOnly, SensitivityRestricted, SensitivityNormal:
		return Sensitivity(raw)
	default:
		return SensitivityLocalOnly
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
func excludedByTrust(sensitivity Sensitivity, tier Tier) (reason ExclusionReason, detail string, excluded bool) {
	switch ResolveSensitivity(string(sensitivity)) {
	case SensitivityLocalOnly:
		return ReasonLocalOnlyWork, "work is local-only: the controller machine is the only place it may run", true
	case SensitivityRestricted:
		if !Satisfies(tier, GateRestricted) {
			return ReasonTierTooLow, "trust_tier " + tierName(tier) + " is below worker-trusted", true
		}
		return "", "", false
	case SensitivityNormal:
		// Falls through to the shared tier check below: normal work
		// imposes no rule of its own, but an unrecognized tier still
		// clears nothing.
		fallthrough
	default:
		// SensitivityNormal imposes no tier rule of its own, but an
		// unrecognized tier still clears nothing: Rank fails closed, and a
		// record carrying a tier this build does not know is a record whose
		// authorization cannot be reasoned about.
		if _, ok := Rank(tier); !ok {
			return ReasonTierTooLow, "trust_tier " + tierName(tier) + " is not a recognized tier", true
		}
		return "", "", false
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
