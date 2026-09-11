package jobs

// Purpose: DagNode and ExecutionDag, the R-16.12/R-16.13 DECIDED node
//
//	field set the AC/S-59.T4 planner produces, plus the DAG validity
//	check (a cycle in deps[] is refused, never silently broken).
//
// Inputs: none at this layer -- Planner.Plan (planner.go) is the sole
//
//	constructor of these types from a PlanInput.
//
// Outputs: DagNode, ExecutionDag, and validateAcyclic's *cascade.Error
//
//	on a cycle.
//
// Constraints: DagNode carries EXACTLY the DECIDED field set
//
//	{capabilities[], deps[], mutable_scope, risk_class, min task class,
//	node_requirements, timeout, cost_ceiling, priority} plus an id --
//	no additional field. A cycle is a typed KindInvalidInput error
//	("not a DAG"), never a dropped edge and never an infinite loop.
//
// SPORT: jobs/dag-planner/ADD (P1-E29-W6-S59-T4).

import (
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DagNode is one execution node: the DECIDED field set verbatim
// (06-FORGE-SPEC.md's plan seam, R-16.12/13) plus the node's own id.
// Capabilities, NodeRequirements, Timeout, CostCeiling and Priority are
// pass-through fields the planner never synthesizes (06 §5.1); Deps,
// MutableScope, RiskClass and MinTaskClass are derived (see planner.go).
type DagNode struct {
	ID string

	Capabilities     []string
	Deps             []string
	MutableScope     []string
	RiskClass        RiskClass
	MinTaskClass     conductor.TaskClass
	NodeRequirements map[string]string
	Timeout          time.Duration
	CostCeiling      float64
	Priority         int
}

// ExecutionDag is the planner's output: a set of DagNodes whose Deps
// reference other nodes' IDs by value. It carries no scheduling state
// (that is T5's ExecutionDag -> advance(dag, event) consumer).
type ExecutionDag struct {
	Nodes []DagNode
}

// NodeByID returns the node with the given id, and whether it was
// found. Not a map because ExecutionDag's own ordering (plan-request
// order) is part of its contract for deterministic downstream use.
func (d ExecutionDag) NodeByID(id string) (DagNode, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return DagNode{}, false
}

// validateAcyclic refuses a DagNode set whose deps[] edges are not a
// DAG. A Go import graph is acyclic at package level, but this ticket's
// own inputs -- ticket depends_on, change footprints -- are supplied by
// the caller and are not guaranteed acyclic, so this check runs on every
// Plan call. Detection is iterative depth-first coloring (white/gray/
// black); a gray node reached again is a back edge, i.e. a cycle.
func validateAcyclic(nodes []DagNode) error {
	byID := make(map[string]DagNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(nodes))

	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch color[id] {
		case black:
			return nil
		case gray:
			return cascade.Newf(cascade.KindInvalidInput,
				"jobs: plan input is not a DAG: cycle through %v", append(path, id))
		}
		color[id] = gray
		node, ok := byID[id]
		if ok {
			for _, dep := range node.Deps {
				if _, exists := byID[dep]; !exists {
					// A dep pointing outside this node set is not this
					// function's concern (planner.go resolves depends_on
					// against the request's own ticket set before this
					// runs); skip rather than fabricate a cycle.
					continue
				}
				if err := visit(dep, append(path, id)); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}

	for _, n := range nodes {
		if color[n.ID] == white {
			if err := visit(n.ID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
