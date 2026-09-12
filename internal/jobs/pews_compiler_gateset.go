package jobs

// Purpose: EffectiveTicketGateSet, the R-21.192 union of a ticket's
//
//	declared gate set (pews_compiler_riskclass.go's DeclaredGateSet) and
//	a footprint classifier's derived gate set (AC/S-59.T4's
//	Planner.Plan, via GateSetForRiskClass on the resulting DagNode.
//	RiskClass), with the classifier's result as an UNLOWERABLE FLOOR.
//	This is a distinct union from riskgates_overlay.go's EffectiveGateSet
//	(which unions a class's table gates with a config overlay) --
//	neither name nor behavior collide; this file defines no second gate
//	table.
//
// Inputs: declared and classifierDerived GateSets.
// Outputs: the de-duplicated union in canonical (riskgates.go) table
//
//	order, or ErrUnclassifiedFootprint.
//
// Constraints: a declared CR-B must never weaken a footprint classified
//
//	High or Critical, so every classifierDerived member is always
//	present in the result -- by construction, not by a runtime
//	comparison, since the result is built by walking the ONE maximal
//	ordered gate list (criticalGates) and keeping any member either
//	input names.
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

// EffectiveTicketGateSet returns the R-21.192 union of declared and
// classifierDerived, in canonical table order, de-duplicated. An empty
// classifierDerived is refused with ErrUnclassifiedFootprint: the
// declared set alone can never stand in for a missing footprint
// classification, regardless of how many gates it names.
func EffectiveTicketGateSet(declared, classifierDerived GateSet) (GateSet, error) {
	if len(classifierDerived) == 0 {
		return nil, ErrUnclassifiedFootprint
	}
	want := make(map[GateItem]bool, len(declared)+len(classifierDerived))
	for _, g := range declared {
		want[g] = true
	}
	for _, g := range classifierDerived {
		want[g] = true
	}
	out := make(GateSet, 0, len(want))
	for _, g := range criticalGates {
		if want[g] {
			out = append(out, g)
		}
	}
	return out, nil
}
