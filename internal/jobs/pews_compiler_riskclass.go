package jobs

// Purpose: the complete R-16.42 CRLevel/QALevel -> RiskClass derivation
//
//	(06 §1a rows 14-15, "cr_level/qa_level -> declared gate set") plus
//	DeclaredGateSet, which resolves that class through AC/S-59.T4's ONE
//	risk-gate table (riskgates.go) as tightened by AH/S-69.T1's
//	tightening-only overlay (riskgates_overlay.go). No gate-set table is
//	defined in this file (R-16.70(b): one risk model).
//
// Inputs: a cr_level string, a qa_level string, a RiskGateOverlay.
// Outputs: RiskClass, or GateSet, or a typed refusal.
//
// Constraints: RiskClassCritical is never derivable from levels --
//
//	Critical is a footprint/domain classification (AC/S-59.T4 rule 1),
//	never a cr_level/qa_level combination. The resolved class is the
//	HIGHER (by risk.go's own riskRank severity ordering) of the
//	cr-derived and qa-derived classes.
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

// crLevelRiskClass maps each R-16.42 canonical cr_level form to its
// RiskClass: CR-B and CR-A+CR-B are Normal; CR-B+CR-C and
// CR-A+CR-B+CR-C are High. An unrecognised form is the caller's concern
// (ValidateContractFields/mapCRLevelToRiskClass check membership first);
// this map is total over validCRLevels' key set.
var crLevelRiskClass = map[string]RiskClass{
	"CR-B":           RiskClassNormal,
	"CR-A+CR-B":      RiskClassNormal,
	"CR-B+CR-C":      RiskClassHigh,
	"CR-A+CR-B+CR-C": RiskClassHigh,
}

// qaLevelRiskClass maps each qa_level value to its RiskClass: QA-A and
// QA-B are Normal, QA-C is High.
var qaLevelRiskClass = map[string]RiskClass{
	"QA-A": RiskClassNormal,
	"QA-B": RiskClassNormal,
	"QA-C": RiskClassHigh,
}

// mapCRLevelToRiskClass is row 14 (cr_level -> declared gate set, first
// half): the R-16.42 form -> RiskClass step. An unrecognised form
// refuses via ErrUnknownReviewLevel rather than defaulting to Normal.
func mapCRLevelToRiskClass(crLevel string) (RiskClass, error) {
	rc, ok := crLevelRiskClass[crLevel]
	if !ok {
		return "", &ErrUnknownReviewLevel{Field: crLevelField, Value: crLevel}
	}
	return rc, nil
}

// mapQALevelToRiskClass is row 15 (qa_level -> declared gate set, first
// half): the qa_level -> RiskClass step.
func mapQALevelToRiskClass(qaLevel string) (RiskClass, error) {
	rc, ok := qaLevelRiskClass[qaLevel]
	if !ok {
		return "", &ErrUnknownReviewLevel{Field: qaLevelField, Value: qaLevel}
	}
	return rc, nil
}

// declaredRiskClass resolves the ticket's declared RiskClass as the
// higher (risk.go's riskRank severity ordering) of the cr-derived and
// qa-derived classes. Never returns RiskClassCritical: both maps above
// are total over {Normal, High} alone.
func declaredRiskClass(crLevel, qaLevel string) (RiskClass, error) {
	crClass, err := mapCRLevelToRiskClass(crLevel)
	if err != nil {
		return "", err
	}
	qaClass, err := mapQALevelToRiskClass(qaLevel)
	if err != nil {
		return "", err
	}
	if riskRank[qaClass] > riskRank[crClass] {
		return qaClass, nil
	}
	return crClass, nil
}

// DeclaredGateSet resolves the ticket-declared RiskClass from crLevel/
// qaLevel and returns its gate set from AC/S-59.T4's GateSetForRiskClass,
// tightened by overlay via AH/S-69.T1's EffectiveGateSet (riskgates_overlay.go).
// No gate-set table is defined here (R-16.70(b)): both calls reuse the
// ONE risk model verbatim.
func DeclaredGateSet(crLevel, qaLevel string, overlay RiskGateOverlay) (GateSet, error) {
	class, err := declaredRiskClass(crLevel, qaLevel)
	if err != nil {
		return nil, err
	}
	return EffectiveGateSet(class, overlay)
}
