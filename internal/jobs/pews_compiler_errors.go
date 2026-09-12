package jobs

// Purpose: the PEWS compiler's fail-closed typed refusals (R-21.192):
//
//	every one of the seventeen contract fields is required, and a
//	missing or unparseable one -- or an unknown model_class/cr_level/
//	qa_level value, or an empty ticket id -- names itself and refuses
//	rather than defaulting or returning a partial PlanInput.
//
// Inputs: none (types/values only).
// Outputs: the sentinel and field-carrying error values
//
//	pews_compiler_fieldmap.go and pews_compiler.go return.
//
// Constraints: every value here is a typed STOP, never a substituted
//
//	default; each Error() names the offending field and, where useful,
//	the expected form, so a caller sees exactly what refused and why.
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

import (
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrEmptyTicketID is returned when PEWSContract.ID is empty. A ticket
// with no id can never be planned (TicketInput.ID is the DAG node's own
// id), so this is refused before any other field is inspected.
var ErrEmptyTicketID = cascade.New(cascade.KindInvalidInput, "jobs: pews contract has no ticket id")

// ErrUnclassifiedFootprint is returned by EffectiveTicketGateSet when
// classifierDerived is empty: the ticket-declared gate set alone can
// never stand in for a missing footprint classification (R-21.192).
var ErrUnclassifiedFootprint = cascade.New(cascade.KindInvalidInput,
	"jobs: classifier-derived gate set is empty; a footprint classification is required before a ticket gate set can be resolved")

// ErrUnknownModelClass is returned when PEWSContract.ModelClass is
// empty or outside the closed {mech,build,heavy,review,arbiter} set
// (06 §4) -- never a default task class, mirroring
// conductor.ModelClassToTaskClass's own fail-closed posture for the
// same case.
type ErrUnknownModelClass struct {
	// ModelClass is the offending raw value (may be empty).
	ModelClass string
}

func (e *ErrUnknownModelClass) Error() string {
	return fmt.Sprintf("jobs: pews contract has unknown model_class %q; expected one of mech, build, heavy, review, arbiter", e.ModelClass)
}

// ErrUnknownReviewLevel is returned when PEWSContract.CRLevel or
// PEWSContract.QALevel is empty or outside its R-16.42 canonical form
// set. Field names which of the two ("cr_level" or "qa_level").
type ErrUnknownReviewLevel struct {
	Field string
	Value string
}

func (e *ErrUnknownReviewLevel) Error() string {
	if e.Field == crLevelField {
		return fmt.Sprintf("jobs: pews contract has unknown cr_level %q; expected one of CR-B, CR-A+CR-B, CR-B+CR-C, CR-A+CR-B+CR-C", e.Value)
	}
	return fmt.Sprintf("jobs: pews contract has unknown qa_level %q; expected one of QA-A, QA-B, QA-C", e.Value)
}

// ErrMissingContractField is returned when a required 06 §1 field is
// absent from the contract (an empty string for a scalar field, or a
// nil slice for a list field -- see PEWSContract's own doc on the
// nil-vs-empty-slice distinction).
type ErrMissingContractField struct {
	Field string
}

func (e *ErrMissingContractField) Error() string {
	return fmt.Sprintf("jobs: pews contract is missing required field %q", e.Field)
}

// ErrUnparseableContractField is returned when a required field is
// present but its value does not have the expected form (for example
// weight outside {XS,S,M,L,XL}). Reason names the expected form.
type ErrUnparseableContractField struct {
	Field  string
	Value  string
	Reason string
}

func (e *ErrUnparseableContractField) Error() string {
	return fmt.Sprintf("jobs: pews contract field %q has value %q, expected %s", e.Field, e.Value, e.Reason)
}
