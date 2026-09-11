package jobs

// Purpose: PlanInput, the ticket|intent union Planner.Plan consumes
//
//	(300-line-cap split from planner.go, per this ticket's own HOW-2).
//
// Inputs: none at this layer -- callers construct TicketInput or
//
//	IntentInput directly; there is no decoder here.
//
// Outputs: PlanInput, TicketInput, IntentInput.
//
// Constraints: TicketInput/IntentInput carry the pass-through node
//
//	fields (Capabilities, NodeRequirements, Timeout, CostCeiling,
//	Priority) VERBATIM -- the planner never synthesizes a value for any
//	of them (06 §5.1 never-invent-scope; R-16.37 absent-constant
//	posture). This file imports no plugins/pbd: AH/S-69.T3 is the PEWS
//	17-field -> PlanInput compiler, a boundary this ticket does not
//	cross.
//
// SPORT: jobs/dag-planner/ADD (P1-E29-W6-S59-T4).

import (
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

// PassThroughFields are the five DagNode fields the planner carries
// verbatim from the plan request, for both TicketInput and IntentInput.
type PassThroughFields struct {
	Capabilities     []string
	NodeRequirements map[string]string
	Timeout          time.Duration
	CostCeiling      float64
	Priority         int
}

// TicketInput plans one ticket into one DagNode. Footprint is the
// declared touched-path set (files_scope ADD+CHANGE+DELETE) -- the
// PRE-image half of the R-21.182 footprint union the risk classifier
// computes; the planner never widens it on the ticket's behalf.
type TicketInput struct {
	ID         string
	ModelClass conductor.ModelClass
	DependsOn  []string
	Footprint  []string

	PassThroughFields
}

// IntentInput plans a single free-text intent into one root DagNode
// with empty Deps and empty MutableScope (no declared footprint exists
// for an intent at plan time).
type IntentInput struct {
	Intent string

	PassThroughFields
}

// PlanInput is the ticket|intent union Planner.Plan accepts. Exactly one
// of Ticket or Intent is set; Planner.Plan returns a typed
// KindInvalidInput error otherwise (empty input, or both set).
type PlanInput struct {
	Ticket *TicketInput
	Intent *IntentInput
}
