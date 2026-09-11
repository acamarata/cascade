package jobs

// Purpose: RaiseRiskClass, the R-21.146 MONOTONIC reclassification
//
//	guard: within one attempt risk may only increase, never lower, and
//	an increase must invalidate the evidence gathered under the weaker
//	gate set and require the expanded lease scope.
//
// Inputs: a prior RiskClass, an observed RiskClass, and the
//
//	RiskGateOverlay in effect.
//
// Outputs: the resulting RiskClass (never below prior), a RiskEscalation
//
//	record on an increase (zero value otherwise), or a fail-closed
//	error for an unrecognised class.
//
// Constraints: an observed class BELOW prior is CLAMPED to prior, never
//
//	an error and never a lowering -- a rename that shrinks the apparent
//	footprint cannot weaken the gate set mid-attempt. ErrUnknownRiskClass
//	(surfaced through EffectiveGateSet/GateSetForRiskClass) propagates
//	fail-closed for an unrecognised prior OR observed class.
//
// SPORT: jobs/risk-reclass-guard/ADD (P1-E34-W7-S69-T1).

// ClassifyFootprint is the exported entry point `cascade policy risk
// explain` uses to classify a footprint outside the DAG planner. It
// calls the SAME classifyFootprint AC/S-59.T4 owns (risk.go, same
// package) -- never a re-derivation -- over the W6 single-repository
// value (singleRepository()), matching the planner's own posture; the
// >=2-repositories gap this ticket cannot close either (see this
// ticket's journal) is the identical absent-constant S-59.T4 and
// S-60.T2 both disclosed.
func ClassifyFootprint(footprint []string, probeRoot string) RiskClass {
	return classifyFootprint(footprint, singleRepository(), probeRoot)
}

// CriticalFloorMatch reports whether p triggers the R-21.182 Critical
// floor and, if so, which category -- exported for the explain
// surface's provenance reporting. Delegates to risk.go's own
// criticalFloorMatch, never a re-derivation.
func CriticalFloorMatch(p string) (category string, matched bool) {
	cat, ok := criticalFloorMatch(p)
	return string(cat), ok
}

// RiskEscalation is the R-21.146 result of a class increase under
// RaiseRiskClass. It is a distinct type from risk_reclassify.go's
// Escalation (AC/S-59.T4's own type, a different field set for a
// different call site -- see this ticket's journal for the naming
// deviation this forced): InvalidatedGateSet names the weaker gate set
// (EffectiveGateSet(prior, ov) in full) whose evidence the escalation
// invalidates, and RequiresExpandedLease is always true on a real
// escalation.
type RiskEscalation struct {
	From                  RiskClass
	To                    RiskClass
	InvalidatedGateSet    GateSet
	RequiresExpandedLease bool
}

// RaiseRiskClass returns the higher of prior and observed (severity
// order, risk.go's riskRank). An observed class at or below prior is
// clamped to prior and reported as no escalation (RiskEscalation{},
// false is never returned as a signal -- callers check the returned
// RiskClass against prior themselves, or rely on RiskEscalation being
// the zero value). An observed class ABOVE prior returns the derived
// class alongside a populated RiskEscalation naming EffectiveGateSet(prior,
// ov) as InvalidatedGateSet and RequiresExpandedLease=true.
// ErrUnknownRiskClass propagates fail-closed for an unrecognised prior
// or observed class before any comparison is made.
func RaiseRiskClass(prior, observed RiskClass, ov RiskGateOverlay) (RiskClass, RiskEscalation, error) {
	if _, err := EffectiveGateSet(prior, ov); err != nil {
		return "", RiskEscalation{}, err
	}
	if _, err := EffectiveGateSet(observed, ov); err != nil {
		return "", RiskEscalation{}, err
	}

	if riskRank[observed] <= riskRank[prior] {
		return prior, RiskEscalation{}, nil
	}

	invalidated, err := EffectiveGateSet(prior, ov)
	if err != nil {
		return "", RiskEscalation{}, err
	}
	return observed, RiskEscalation{
		From:                  prior,
		To:                    observed,
		InvalidatedGateSet:    invalidated,
		RequiresExpandedLease: true,
	}, nil
}
