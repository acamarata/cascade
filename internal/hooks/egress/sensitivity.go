package egress

import "github.com/acamarata/cascade/pkg/cascade"

// SensitivityPass decides whether content classified at tier may be
// written on a class configured by cfg. It returns nil to admit and an
// ErrSensitivityViolation-wrapped error to refuse.
//
// The matrix, in the order the cases are evaluated:
//
//	local-only  admitted only when the registrant set AllowLocalOnly.
//	restricted  admitted only when the registrant set AllowRestricted.
//	internal    admitted on any enabled class.
//	public      admitted always.
//
// An unset or unrecognised tier resolves to restricted before the matrix
// runs, so the permissive answer is never the default one. AllowedTiers,
// when the registrant supplied it, narrows the verdict further and can
// only ever refuse.
//
// This function does not consult Enabled: Intercept refuses a disabled
// class before it gets here, and duplicating that check would give two
// places to change it.
func SensitivityPass(class EgressClass, cfg InterceptConfig, tier SensitivityTier) error {
	resolved := tier.Resolve()
	if err := matrixVerdict(class, cfg, tier, resolved); err != nil {
		return err
	}
	if !tierListed(cfg.AllowedTiers, resolved) {
		return sensitivityViolation(class, cfg, tier, resolved, "the class narrows egress to an explicit tier list")
	}
	return nil
}

// matrixVerdict applies the four-case matrix.
func matrixVerdict(class EgressClass, cfg InterceptConfig, declared, resolved SensitivityTier) error {
	switch resolved {
	case TierLocalOnly:
		if !cfg.AllowLocalOnly {
			return sensitivityViolation(class, cfg, declared, resolved,
				"the class was not registered to admit local-only content")
		}
	case TierRestricted:
		if !cfg.AllowRestricted {
			return sensitivityViolation(class, cfg, declared, resolved,
				"the class was not registered to admit restricted content")
		}
	case TierInternal, TierPublic:
	case TierUnset:
		// Unreachable: Resolve maps the unset tier to restricted before
		// this switch runs. It is named rather than folded into default
		// so the exhaustiveness check proves every declared tier was
		// considered here, and it refuses rather than falls through.
		return sensitivityViolation(class, cfg, declared, resolved, "the unset tier reached the matrix unresolved")
	default:
		return sensitivityViolation(class, cfg, declared, resolved, "unrecognised tier")
	}
	return nil
}

// tierListed reports whether resolved is admitted by an explicit
// AllowedTiers list. An empty list means "the matrix alone decides", not
// "nothing is allowed": the narrowing is opt-in.
func tierListed(allowed []SensitivityTier, resolved SensitivityTier) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, tier := range allowed {
		if tier == resolved {
			return true
		}
	}
	return false
}

// sensitivityViolation builds the refusal. It names the declared tier and
// the tier it resolved to, because "restricted" appearing in a refusal a
// caller believes it classified as internal is the single most confusing
// outcome this gate can produce.
func sensitivityViolation(class EgressClass, cfg InterceptConfig, declared, resolved SensitivityTier, why string) error {
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrSensitivityViolation,
		"egress: class %q (owner %s) refused tier %q (resolved %q): %s",
		string(class), cfg.Owner, string(declared), string(resolved), why)
}
