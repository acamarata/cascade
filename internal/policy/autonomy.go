// Package policy (autonomy.go): Purpose: the autonomy profile — layer 5
//
//	of the R-14.26 evaluation order, which supplies the DEFAULT verdict for
//	a risk level when no deny-list, elevation, grant or capability rule
//	matched. It is the top of the authorization stack and therefore the
//	one layer that must never be able to release what a layer beneath it
//	refused.
//
// Inputs: a RiskLevel (types.go, already resolved once through the
//
//	CommandClassifier seam per R-14.26), and a profile name plus optional
//	per-level overlays from the [policy] config section (autonomy_config.go).
//
// Outputs: Verdict (the single canonical enum of R-14.27), Slot (a
//
//	resolved per-level decision carrying the AutoAdvance ceiling flag and a
//	source annotation), and AutonomyProfile with its five L0-L4 slots.
//
// Constraints: FAIL CLOSED on every axis, and RESTRICT-ONLY on top of that.
//
//	Verdict's iota starts at 1 so an unset slot does not read as Allow, and
//	safeVerdict maps every invalid value to Deny — the same shape as
//	types.go's safeLevel and safeActionClass, which this file reuses rather
//	than re-deriving wherever a RiskLevel is involved. A nil profile
//	resolves to deny-everything, so a missing or unresolvable profile is
//	the most restrictive behaviour and never the most permissive. Two
//	hardcoded ceilings sit ABOVE every profile and every overlay and cannot
//	be raised by either: L4 is always Deny (06-FORGE-SPEC §5.15 — "deny-list,
//	same-turn authorization only"), and AutoAdvance is true only on L0/L1
//	slots that resolved to Allow (§5.15 auto-advance ceiling).
//
// SPORT: internal/policy Verdict/ADDED, Slot/ADDED, SlotSource/ADDED,
//
//	AutonomyProfile/ADDED (P1-E09-W2-S18-T1).
package policy

// Verdict is the single canonical decision enum (R-14.27): what happens to
// an action. There is no fourth value: "never-auto" is the AutoAdvance
// ceiling flag on a resolved Slot, not a verdict, and ElevationRequired is
// a separate field on S-18.T4's dry-run result.
//
// The zero value is deliberately not a member, for the same reason
// RiskLevel's is not: a slot left unset by a decoder, a config path or a
// forgotten field must read as Deny, never as Allow. Numeric order is
// restrictiveness order, which is what makes maxVerdict a correct
// combinator.
type Verdict uint8

// The three verdicts, least to most restrictive.
const (
	_ Verdict = iota // 0 is deliberately not a valid Verdict

	// VerdictAllow permits the action without asking.
	VerdictAllow
	// VerdictAsk permits the action only after the operator approves it.
	VerdictAsk
	// VerdictDeny refuses the action outright.
	VerdictDeny
)

// verdictNames holds the stable wire name of each verdict, indexed by
// value. Index 0 is the invalid zero value's placeholder.
var verdictNames = [...]string{"", "allow", "ask", "deny"}

// String returns the verdict's stable name, e.g. "ask". An invalid value
// renders as "invalid-verdict" so a bad value can never be mistaken for a
// real decision in a log or an audit row.
func (v Verdict) String() string {
	if !v.Valid() {
		return "invalid-verdict"
	}
	return verdictNames[v]
}

// Valid reports whether v names one of the three verdicts.
func (v Verdict) Valid() bool { return v >= VerdictAllow && v <= VerdictDeny }

// safeVerdict maps any Verdict to one that is safe to act on: a valid
// verdict passes through, and the zero value or anything out of range
// becomes Deny.
//
// This is a third helper of the same shape as types.go's safeLevel and
// safeActionClass, and it exists because neither of those fits: they are
// total functions over their OWN enums (RiskLevel and ActionClass), and Go
// has no way to reuse one for a different named type. Every place in this
// ticket where a RiskLevel needs the treatment calls safeLevel directly
// rather than duplicating it — this twin covers only the enum R-14.27 adds.
func safeVerdict(v Verdict) Verdict {
	if !v.Valid() {
		return VerdictDeny
	}
	return v
}

// maxVerdict returns the more restrictive of a and b, after passing both
// through safeVerdict. It is the combinator that makes "restrict, never
// widen" mechanical: a layer that wants to tighten a decision combines with
// maxVerdict, and no combination of inputs can produce something more
// permissive than either input.
func maxVerdict(a, b Verdict) Verdict {
	sa, sb := safeVerdict(a), safeVerdict(b)
	if sa > sb {
		return sa
	}
	return sb
}

// SlotSource annotates where a resolved slot's verdict came from, so
// `cascade config list --effective` and the audit journal can show an
// operator why a level decides the way it does.
type SlotSource uint8

// The three sources, in the order a value can override the one before it.
const (
	_ SlotSource = iota // 0 is deliberately not a valid SlotSource

	// SourceProfileDefault means the named profile's own table set it.
	SourceProfileDefault
	// SourceOverlay means a [policy] allow/ask/deny overlay list set it.
	SourceOverlay
	// SourceHardcoded means a ceiling in this file set it, above and
	// beyond whatever the profile and overlays asked for.
	SourceHardcoded
)

// slotSourceNames holds the stable name of each source, indexed by value.
var slotSourceNames = [...]string{"", "profile-default", "overlay", "hardcoded"}

// String returns the source's stable name, e.g. "overlay".
func (s SlotSource) String() string {
	if s < SourceProfileDefault || s > SourceHardcoded {
		return "invalid-slot-source"
	}
	return slotSourceNames[s]
}

// Slot is one resolved L0-L4 decision: the verdict, the auto-advance
// ceiling flag, and where the verdict came from.
//
// AutoAdvance is NOT a verdict value (R-14.27). It answers a second,
// narrower question: may an autonomous loop proceed past this action
// without a human turn. §5.15 permits that on L0/L1 only, under an
// allow-tier policy, so an Ask or Deny slot never carries it and neither
// does any slot above L1 — "never-auto" for L3 is exactly this flag being
// false on an Ask slot.
type Slot struct {
	// Verdict is what happens to an action at this level.
	Verdict Verdict `json:"verdict"`
	// AutoAdvance reports whether an autonomous loop may continue past
	// this level without asking.
	AutoAdvance bool `json:"auto_advance"`
	// Source annotates which layer produced Verdict.
	Source SlotSource `json:"source"`
}

// AutonomyProfile is a named, fully resolved L0-L4 verdict table.
//
// A profile is immutable once built: the config path builds a new one and
// the controller swaps the pointer (autonomy_controller.go), so a running
// profile is never mutated under a reader and there is no resolution cache
// to invalidate.
type AutonomyProfile struct {
	name  string
	slots [len(riskLevelNames)]Slot // indexed by RiskLevel; index 0 unused
}

// Name returns the profile's name. The nil profile is named "locked",
// which is what it behaves as.
func (p *AutonomyProfile) Name() string {
	if p == nil {
		return lockedProfileName
	}
	return p.name
}

// SlotFor returns the resolved slot for level, with both hardcoded
// ceilings applied.
//
// A nil profile — an unset profile, a profile that failed to resolve, a
// zero-valued struct field — denies at every level. This is the single
// most important line in the file: the catastrophic failure this design
// guards against is a profile that goes missing and is read as full
// autonomy.
func (p *AutonomyProfile) SlotFor(level RiskLevel) Slot {
	lvl := safeLevel(level)
	if p == nil {
		return Slot{Verdict: VerdictDeny, AutoAdvance: false, Source: SourceHardcoded}
	}
	return clampSlot(lvl, p.slots[lvl])
}

// VerdictFor returns the resolved verdict for level. It is the API the
// policy engine (engine.go, layer 5) calls and the only autonomy source it
// consults.
func (p *AutonomyProfile) VerdictFor(level RiskLevel) Verdict {
	return p.SlotFor(level).Verdict
}

// AllowsAutoAdvance reports whether an autonomous loop may proceed past an
// action at level without a human turn (§5.15). It is deliberately a
// separate question from the verdict, so a caller cannot satisfy the
// ceiling by reading the verdict alone.
func (p *AutonomyProfile) AllowsAutoAdvance(level RiskLevel) bool {
	return p.SlotFor(level).AutoAdvance
}

// clampSlot applies the two hardcoded ceilings to a slot the profile or an
// overlay proposed. Both are one-directional: they can only make a slot
// more restrictive, never less, so no profile table and no config file can
// widen past them.
//
//  1. L4 is always Deny (§5.15: destructive/privileged is deny-list,
//     same-turn authorization only — a standing profile cannot pre-approve
//     it).
//  2. AutoAdvance survives only on an L0/L1 slot that resolved to Allow
//     (§5.15 auto-advance ceiling); everywhere else it is cleared.
func clampSlot(level RiskLevel, s Slot) Slot {
	lvl := safeLevel(level)
	out := s
	out.Verdict = safeVerdict(s.Verdict)
	if !s.Verdict.Valid() {
		out.Source = SourceHardcoded
	}
	if floor := hardVerdictFloor(lvl); maxVerdict(out.Verdict, floor) != out.Verdict {
		out.Verdict = maxVerdict(out.Verdict, floor)
		out.Source = SourceHardcoded
	}
	if out.AutoAdvance && !autoAdvanceEligible(lvl, out.Verdict) {
		out.AutoAdvance = false
		out.Source = SourceHardcoded
	}
	return out
}

// hardVerdictFloor is the least restrictive verdict any profile may hold
// at level. Only L4 has a floor above Allow; the rest of the ladder is
// governed by the profile table, whose own defaults are already stricter
// than this floor.
func hardVerdictFloor(level RiskLevel) Verdict {
	if safeLevel(level) == L4 {
		return VerdictDeny
	}
	return VerdictAllow
}

// autoAdvanceEligible reports whether a slot at level with verdict v may
// carry AutoAdvance: L0/L1 only, and only under an allow-tier verdict
// (§5.15). An Ask slot never auto-advances, which is what makes L3's
// "ask, never auto" the default rather than a special case.
func autoAdvanceEligible(level RiskLevel, v Verdict) bool {
	lvl := safeLevel(level)
	return (lvl == L0 || lvl == L1) && safeVerdict(v) == VerdictAllow
}
