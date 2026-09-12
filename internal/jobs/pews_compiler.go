package jobs

// Purpose: CompileTicket, the pure PEWS 17-field-to-PlanInput compiler
//
//	AC/S-59.T4 named this ticket as the boundary consumer for. Builds a
//	PlanInput wrapping AC/S-59.T4's own TicketInput shape verbatim (no
//	new PlanInput variant), and returns the ticket's DECLARED gate set
//	(pews_compiler_riskclass.go) alongside it. The classifier-derived
//	gate set and the R-21.192 union with it (EffectiveTicketGateSet)
//	remain the caller's job: that union needs Planner.Plan's result,
//	which needs a SessionScope this pure input does not carry.
//
// Inputs: a PEWSContract and a RiskGateOverlay.
// Outputs: a PlanInput, the declared GateSet, or a typed refusal.
//
// Constraints: never imports plugins/pbd/internal/pews (06 §5.1;
//
//	02-TARGET-STRUCTURE v1.1 import boundary). Adds no SessionScope
//	resolution, Planner.Plan invocation, scheduling, leases, worktrees,
//	CLI/RPC surface, or YAML decoding (all out of this ticket's scope;
//	the end-to-end wiring is AH/S-70.T1's acceptance ticket).
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

// CompileTicket validates c against every one of the seventeen 06 §1
// fields (ValidateContractFields), then builds a PlanInput wrapping
// jobs.TicketInput{ID, ModelClass, DependsOn, Footprint} -- AC/S-59.T4's
// own field shape, reused verbatim -- and resolves the ticket-declared
// gate set from c.CRLevel/c.QALevel through the ONE risk-gate model
// (DeclaredGateSet), tightened by overlay. A malformed or missing field
// refuses before any output is constructed; CompileTicket never returns
// a partial PlanInput.
func CompileTicket(c PEWSContract, overlay RiskGateOverlay) (PlanInput, GateSet, error) {
	if err := ValidateContractFields(c); err != nil {
		return PlanInput{}, nil, err
	}

	declared, err := DeclaredGateSet(c.CRLevel, c.QALevel, overlay)
	if err != nil {
		return PlanInput{}, nil, err
	}

	ticket := &TicketInput{
		ID:         mapIDToTicketID(c),
		ModelClass: c.ModelClass,
		DependsOn:  mapDependsOnToDAGEdges(c),
		Footprint:  mapFilesScopeToFootprint(c),
	}
	return PlanInput{Ticket: ticket}, declared, nil
}
