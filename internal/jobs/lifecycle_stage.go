package jobs

// Purpose: LifecycleStage, the R-16.13 DECIDED 13-stage dev-shop
//
//	lifecycle sequence as DATA (never prose prompts), plus
//	ReclassificationStages, the R-21.146 three mandatory reclassification
//	points expressed over the same enum.
//
// Inputs: none at this layer -- LifecycleStages/ReclassificationStages
//
//	return fixed data; ParseLifecycleStage takes a raw string.
//
// Outputs: the ordered []LifecycleStage slices, or ErrUnknownLifecycleStage
//
//	for any input outside the 13.
//
// Constraints: no permissive zero value -- an empty or unrecognised
//
//	string never resolves to a stage; LifecycleStages/ReclassificationStages
//	return literal data, never a recomputed/derived list, so a later
//	sequence change is a one-line edit here rather than a scattered fix.
//
// SPORT: jobs/lifecycle-stage/ADD (P1-E34-W7-S69-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// LifecycleStage is the closed R-16.13 dev-shop lifecycle vocabulary.
type LifecycleStage string

// The 13 R-16.13 DECIDED stages, in written order. This is the ONLY
// list; LifecycleStages returns it verbatim.
const (
	StageIntent         LifecycleStage = "intent"
	StageScope          LifecycleStage = "scope"
	StagePlan           LifecycleStage = "plan"
	StageDecomposeLease LifecycleStage = "decompose_lease"
	StageImplement      LifecycleStage = "implement"
	StageCR             LifecycleStage = "cr"
	StageQA             LifecycleStage = "qa"
	StageAdversarial    LifecycleStage = "adversarial"
	StageIntegrate      LifecycleStage = "integrate"
	StageCleanNodeCI    LifecycleStage = "clean_node_ci"
	StageReleaseCDGate  LifecycleStage = "release_cd_gate"
	StageAccept         LifecycleStage = "accept"
	StageLearn          LifecycleStage = "learn"
)

// lifecycleStages is the single source LifecycleStages/ParseLifecycleStage
// both read -- data, never recomputed.
var lifecycleStages = []LifecycleStage{
	StageIntent, StageScope, StagePlan, StageDecomposeLease, StageImplement,
	StageCR, StageQA, StageAdversarial, StageIntegrate, StageCleanNodeCI,
	StageReleaseCDGate, StageAccept, StageLearn,
}

// LifecycleStages returns the 13 R-16.13 stages in written order.
func LifecycleStages() []LifecycleStage {
	return append([]LifecycleStage{}, lifecycleStages...)
}

// reclassificationStages is R-21.146's three mandatory reclassification
// points, in sequence order: plan time, every lease checkpoint reached
// during implement, and immediately before acceptance.
var reclassificationStages = []LifecycleStage{StagePlan, StageImplement, StageAccept}

// ReclassificationStages returns exactly {StagePlan, StageImplement,
// StageAccept} in sequence order -- R-21.146's plan-time, every-lease-
// checkpoint and pre-acceptance points. Reclassification is therefore
// never plan-time-only.
func ReclassificationStages() []LifecycleStage {
	return append([]LifecycleStage{}, reclassificationStages...)
}

// ErrUnknownLifecycleStage is returned by ParseLifecycleStage for any
// input outside the 13 R-16.13 stages.
var ErrUnknownLifecycleStage = cascade.New(cascade.KindInvalidInput, "jobs: unknown lifecycle stage")

// ParseLifecycleStage parses s into one of the 13 R-16.13 stages.
// Fail-closed: there is no permissive zero value, so an unrecognised
// string is ErrUnknownLifecycleStage rather than a zero-value stage.
func ParseLifecycleStage(s string) (LifecycleStage, error) {
	for _, stage := range lifecycleStages {
		if string(stage) == s {
			return stage, nil
		}
	}
	return "", ErrUnknownLifecycleStage
}
