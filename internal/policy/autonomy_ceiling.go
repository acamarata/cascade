// Purpose: `auto_advance_ceiling` — the profile's MASTER SWITCH for tier-1
//
//	auto-approval, and the one knob an operator must turn on deliberately.
//
// WHY A SECOND KNOB IS NOT A DUPLICATE. The profile's per-level slots
//
//	already answer "may an action at THIS risk level auto-advance"
//	(AllowsAutoAdvance). This answers a different question: has the
//	operator opted into auto-advance at all? Without it, a profile whose
//	slots happen to permit L0/L1 would start auto-approving the moment the
//	evaluator landed, which is a behaviour change nobody asked for. Default
//	disabled means the feature arrives switched off.
//
// WHAT IT IS NOT. It never selects the supervision TIER — that is
//
//	`[fleet.supervision].tier`, config, per R-16.54 (superseding R-14.55 on
//	this point). Nothing derives a tier from this field and nothing derives
//	this field from a tier.
//
// Inputs: the `auto_advance_ceiling` key under [policy].
// Outputs: a Ceiling, defaulting to CeilingDisabled.
// Constraints: an unrecognised value is a config ERROR, never a silent
//
//	fallback. A typo that resolved to "tier3" by accident would be the
//	worst possible failure of this file.
//
// SPORT: internal/policy:autonomy-ceiling (ADD) — P1-E18-W4-S39-T2.
package policy

import "strings"

// Ceiling is how far this profile permits unattended advancement.
type Ceiling string

// The four ceiling values, least to most permissive.
const (
	// CeilingDisabled is the default and the zero value: no auto-advance.
	CeilingDisabled Ceiling = "disabled"
	// CeilingTier1 permits tier-1 auto-approval of L0/L1 actions.
	CeilingTier1 Ceiling = "tier1"
	// CeilingTier2 is reserved for the PTY-attached tier (R/S-39.T3).
	CeilingTier2 Ceiling = "tier2"
	// CeilingTier3 is reserved for the suggest-only tier (R/S-39.T3).
	CeilingTier3 Ceiling = "tier3"
)

// ceilingRank orders the ceilings. Declared as a table rather than an
// integer type so the wire value stays a readable name in config and audit.
var ceilingRank = map[Ceiling]int{
	CeilingDisabled: 0,
	CeilingTier1:    1,
	CeilingTier2:    2,
	CeilingTier3:    3,
}

// Valid reports whether c is one of the four declared values.
func (c Ceiling) Valid() bool {
	_, ok := ceilingRank[c]
	return ok
}

// AllowsTier1 reports whether c permits tier-1 auto-approval.
//
// An INVALID ceiling reports false. That is the same direction every other
// unreadable value in this package resolves in: a value nobody can read is
// never permission.
func (c Ceiling) AllowsTier1() bool {
	rank, ok := ceilingRank[c]
	return ok && rank >= ceilingRank[CeilingTier1]
}

// String renders the ceiling, mapping the empty zero value to its real
// name so a record never shows a blank field.
func (c Ceiling) String() string {
	if c == "" {
		return string(CeilingDisabled)
	}
	return string(c)
}

// parseCeiling reads the `auto_advance_ceiling` key.
//
// An absent key is the default; a present key must name one of the four
// values exactly. Case is normalised because TOML operators type by hand,
// but nothing else is guessed at.
func parseCeiling(table map[string]interface{}) (Ceiling, error) {
	raw, present := table[ceilingKey]
	if !present {
		return CeilingDisabled, nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", newConfigError(ceilingKey, "must be a string")
	}
	candidate := Ceiling(strings.ToLower(strings.TrimSpace(text)))
	if !candidate.Valid() {
		return "", newConfigError(ceilingKey,
			"must be one of disabled, tier1, tier2, tier3 (got %q)", text)
	}
	return candidate, nil
}

// ceilingKey is the [policy] key this file owns.
const ceilingKey = "auto_advance_ceiling"
