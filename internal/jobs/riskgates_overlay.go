package jobs

// Purpose: RiskGateOverlay, the R-16.70(b) tightening-only
//
//	`[policy.risk_gates]` config overlay over AC/S-59.T4's ONE gate-set
//	table (riskgates.go), plus EffectiveGateSet, which unions the
//	table's result with the overlay's per-class additions. This file
//	defines NO second gate-set table and NO second GateItem enum --
//	both stay riskgates.go's, verbatim.
//
// Inputs: a RiskClass and a RiskGateOverlay (EffectiveGateSet); a raw
//
//	gate-step name string (ParseGateItem).
//
// Outputs: GateSet, or a fail-closed error.
//
// Constraints: TIGHTENING-ONLY BY CONSTRUCTION: RiskGateOverlay's value
//
//	type expresses per-class ADDITIONS only (a []GateItem to union in),
//	so a removal is not representable in the type at all --
//	EffectiveGateSet's output is therefore always a superset of
//	GateSetForRiskClass's result and of the R-21.182 Critical floor's
//	gate set, for every overlay value. ParseGateItem validates against
//	riskgates.go's own maximal gate set (criticalGates, the union of
//	all four classes) rather than a second enumeration, so a name this
//	ticket would accept and one AC/S-59.T4 would accept can never
//	diverge.
//
// SPORT: jobs/risk-gate-overlay/ADD (P1-E34-W7-S69-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// RiskGateOverlay is the decoded `[policy.risk_gates]` config value: at
// most one entry per RiskClass, each naming the gates that class's
// table default does not already include. An absent class key means no
// addition for that class -- the zero value (nil map) is the empty
// overlay, under which EffectiveGateSet(rc, nil) == GateSetForRiskClass(rc).
type RiskGateOverlay map[RiskClass]GateSet

// ParseGateItem validates a raw config string against riskgates.go's
// own gate-step vocabulary (criticalGates, the maximal DECIDED gate
// set -- every valid GateItem is a criticalGates member by
// construction, since criticalGates is highGates plus its own three
// items, which is normalGates plus its own four, which is lowGates
// plus its own three). An unrecognised name is a typed refusal, never
// a silently dropped entry.
func ParseGateItem(s string) (GateItem, error) {
	for _, g := range criticalGates {
		if string(g) == s {
			return g, nil
		}
	}
	return "", cascade.Newf(cascade.KindInvalidInput, "jobs: unknown gate step %q", sanitizeGateStep(s))
}

// sanitizeGateStep bounds an unrecognised gate-step name's length in an
// error message, so a pathological config value cannot inflate a log
// line or a CLI error unboundedly.
func sanitizeGateStep(s string) string {
	const maxLen = 64
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// BuildRiskGateOverlay converts the raw class-name -> gate-step-name
// lists internal/policy's config parser decodes ([policy.risk_gates],
// R-16.70(b)) into a typed RiskGateOverlay. internal/policy cannot
// import this package directly (internal/jobs already imports
// internal/conductor, which imports internal/secrets, which imports
// internal/policy -- an import cycle), so the raw string-keyed shape
// crosses the package boundary and this function is where the typed
// gate-step validation this ticket's own HOW-2 names actually happens.
// An unparseable gate-step name is a typed refusal, never a silently
// dropped entry; the caller (cmd/cascade's daemon composition root,
// which imports both packages) runs this BEFORE the config reload
// swap, so a bad name never reaches a running Controller
// (validate-before-write).
func BuildRiskGateOverlay(raw map[string][]string) (RiskGateOverlay, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(RiskGateOverlay, len(raw))
	for className, names := range raw {
		rc := RiskClass(className)
		if _, err := GateSetForRiskClass(rc); err != nil {
			return nil, err
		}
		gates := make(GateSet, 0, len(names))
		for _, name := range names {
			g, err := ParseGateItem(name)
			if err != nil {
				return nil, err
			}
			gates = append(gates, g)
		}
		out[rc] = gates
	}
	return out, nil
}

// EffectiveGateSet returns AC/S-59.T4's GateSetForRiskClass(rc) result
// UNION ov's additions for rc, in the table's canonical order followed
// by the overlay's declared order, de-duplicated. An overlay entry
// already present in the table's own set is a no-op (still de-
// duplicated, never appended twice). ErrUnknownRiskClass (via
// GateSetForRiskClass) propagates unchanged -- fail-closed for an
// unrecognised class regardless of what the overlay carries.
func EffectiveGateSet(rc RiskClass, ov RiskGateOverlay) (GateSet, error) {
	base, err := GateSetForRiskClass(rc)
	if err != nil {
		return nil, err
	}
	additions := ov[rc]
	if len(additions) == 0 {
		return base, nil
	}
	seen := make(map[GateItem]bool, len(base)+len(additions))
	out := make(GateSet, 0, len(base)+len(additions))
	for _, g := range base {
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, g := range additions {
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out, nil
}
