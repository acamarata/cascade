package jobs

// Purpose: the RiskClass -> gate-set DATA table (R-16.13, R-16.70b: this
//
//	file plus risk.go are the ONE risk model AC/S-59.T4 owns). 300-line-
//	cap split from risk.go.
//
// Inputs: a RiskClass.
//
// Outputs: GateSet, the DECIDED gate items for that class.
//
// Constraints: the four gate sets are the DECIDED table VERBATIM (Low
//
//	format+static+targeted verification; Normal adds build+lint+
//	targeted tests+code review+integration checks; High adds
//	independent QA+adversarial review+affected/full integration CI+
//	clean-node verification; Critical adds explicit human approval+
//	rollback evidence+release gate) -- each class's set is its own PLUS
//	every lower class's set (additive), matching the severity ordering
//	risk.go's riskRank gives RiskClass. AH/S-69.T1 owns the
//	policy-table-as-data form this ticket's mapping encodes.
//
// SPORT: jobs/risk-classifier/ADD (P1-E29-W6-S59-T4).

import "github.com/acamarata/cascade/pkg/cascade"

// GateItem is one named verification gate in a RiskClass's gate set.
type GateItem string

// The closed set of gate names the four RiskClass gate sets below
// compose from, verbatim from the DECIDED table (see this file's
// Purpose comment).
const (
	GateFormat                  GateItem = "format"
	GateStatic                  GateItem = "static"
	GateTargetedVerification    GateItem = "targeted_verification"
	GateBuild                   GateItem = "build"
	GateLint                    GateItem = "lint"
	GateTargetedTests           GateItem = "targeted_tests"
	GateCodeReview              GateItem = "code_review"
	GateIntegrationChecks       GateItem = "integration_checks"
	GateIndependentQA           GateItem = "independent_qa"
	GateAdversarialReview       GateItem = "adversarial_review"
	GateAffectedFullIntegration GateItem = "affected_full_integration_ci"
	GateCleanNodeVerification   GateItem = "clean_node_verification"
	GateHumanApproval           GateItem = "human_approval"
	GateRollbackEvidence        GateItem = "rollback_evidence"
	GateReleaseGate             GateItem = "release_gate"
)

// GateSet is an ordered, deduplicated list of GateItems.
type GateSet []GateItem

var (
	lowGates      = GateSet{GateFormat, GateStatic, GateTargetedVerification}
	normalGates   = append(append(GateSet{}, lowGates...), GateBuild, GateLint, GateTargetedTests, GateCodeReview, GateIntegrationChecks)
	highGates     = append(append(GateSet{}, normalGates...), GateIndependentQA, GateAdversarialReview, GateAffectedFullIntegration, GateCleanNodeVerification)
	criticalGates = append(append(GateSet{}, highGates...), GateHumanApproval, GateRollbackEvidence, GateReleaseGate)
)

// GateSetForRiskClass returns the DECIDED gate set for class. An
// unknown class fails closed with a typed error -- never a permissive
// default gate set (a caller with a bad class must never run under the
// lightest gate).
func GateSetForRiskClass(class RiskClass) (GateSet, error) {
	switch class {
	case RiskClassLow:
		return append(GateSet{}, lowGates...), nil
	case RiskClassNormal:
		return append(GateSet{}, normalGates...), nil
	case RiskClassHigh:
		return append(GateSet{}, highGates...), nil
	case RiskClassCritical:
		return append(GateSet{}, criticalGates...), nil
	default:
		return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: unknown risk class %q", class)
	}
}
