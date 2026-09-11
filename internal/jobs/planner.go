package jobs

// Purpose: Planner.Plan(ctx, input, scope), the conductor.plan seam
//
//	(06-FORGE-SPEC.md's DECIDED plan seam name; the RPC alias over this
//	Go seam is AP/S-82.T1's, R-21.39). Turns a PlanInput plus a required
//	SessionScope into a single-node ExecutionDag.
//
// Inputs: ctx, a PlanInput (ticket|intent union), and a REQUIRED
//
//	SessionScope (E/S-08.T4) resolving the repository root the risk
//	classifier's content probes read.
//
// Outputs: ExecutionDag, or a typed KindInvalidInput error for a
//
//	dependency cycle, an unknown model_class, or an empty input.
//
// Constraints: no scheduling/admission/advance (T5), no lease
//
//	acquisition (T2), no worktree management (T3), no CLI/RPC surface
//	(S-60.T1), no PEWS compilation (no plugins/pbd import -- AH/S-69.T3).
//
// SPORT: jobs/dag-planner/ADD (P1-E29-W6-S59-T4).

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Planner is the DAG planner. The zero value is usable when no
// reachability expansion is needed; use NewPlanner to inject one.
type Planner struct {
	reachFn ReachabilityFn
}

// NewPlanner constructs a Planner. reachFn may be nil (the W6 default:
// no reachability expansion, per R-21.257).
func NewPlanner(reachFn ReachabilityFn) *Planner {
	return &Planner{reachFn: reachFn}
}

// Plan turns input into a single-node ExecutionDag. A ticket input
// yields one node whose Deps come from DependsOn, MutableScope from
// Footprint, and MinTaskClass from ModelClassToTaskClass(ModelClass) --
// an unknown ModelClass is a typed error, never a default task class.
// An intent input yields one root node with empty Deps and
// MutableScope. Both carry the pass-through fields verbatim.
func (pl *Planner) Plan(ctx context.Context, input PlanInput, sc scope.SessionScope) (ExecutionDag, error) {
	switch {
	case input.Ticket != nil && input.Intent != nil:
		return ExecutionDag{}, cascade.New(cascade.KindInvalidInput,
			"jobs: plan input carries both a ticket and an intent; exactly one is required")
	case input.Ticket != nil:
		return pl.planTicket(ctx, *input.Ticket, sc)
	case input.Intent != nil:
		return pl.planIntent(ctx, *input.Intent)
	default:
		return ExecutionDag{}, cascade.New(cascade.KindInvalidInput,
			"jobs: plan input is empty; a ticket or an intent is required")
	}
}

func (pl *Planner) planTicket(ctx context.Context, t TicketInput, sc scope.SessionScope) (ExecutionDag, error) {
	if t.ID == "" {
		return ExecutionDag{}, cascade.New(cascade.KindInvalidInput, "jobs: ticket input has no id")
	}
	taskClass, err := conductor.ModelClassToTaskClass(t.ModelClass)
	if err != nil {
		return ExecutionDag{}, err
	}

	node := DagNode{
		ID:               t.ID,
		Capabilities:     t.Capabilities,
		Deps:             t.DependsOn,
		MutableScope:     t.Footprint,
		MinTaskClass:     taskClass,
		NodeRequirements: t.NodeRequirements,
		Timeout:          t.Timeout,
		CostCeiling:      t.CostCeiling,
		Priority:         t.Priority,
	}

	if err := validateAcyclic([]DagNode{node}); err != nil {
		return ExecutionDag{}, err
	}

	node.RiskClass = pl.classify(ctx, t.Footprint, probeRootOf(sc))
	return ExecutionDag{Nodes: []DagNode{node}}, nil
}

func (pl *Planner) planIntent(ctx context.Context, in IntentInput) (ExecutionDag, error) {
	if in.Intent == "" {
		return ExecutionDag{}, cascade.New(cascade.KindInvalidInput, "jobs: intent input is empty")
	}
	node := DagNode{
		ID:               "intent:" + in.Intent,
		Capabilities:     in.Capabilities,
		Deps:             nil,
		MutableScope:     nil,
		MinTaskClass:     "",
		NodeRequirements: in.NodeRequirements,
		Timeout:          in.Timeout,
		CostCeiling:      in.CostCeiling,
		Priority:         in.Priority,
		RiskClass:        pl.classify(ctx, nil, ""),
	}
	return ExecutionDag{Nodes: []DagNode{node}}, nil
}

// classify runs the R-21.182 footprint union followed by the R-16.37
// classifier. A reachability-seam error fails closed to Critical (see
// unionFootprint/classifyFootprint doc) rather than failing Plan
// itself: Plan's own typed error list is exactly {cycle, unknown
// model_class, empty input}, and a reachability failure is not one of
// them -- it is an input the classifier has a defined answer for.
func (pl *Planner) classify(ctx context.Context, footprint []string, probeRoot string) RiskClass {
	union, err := unionFootprint(ctx, footprint, pl.reachFn)
	if err != nil {
		return RiskClassCritical
	}
	return classifyFootprint(union, singleRepository(), probeRoot)
}

// singleRepository is the W6 repositories value: a plan input's
// classifier always resolves to the one repository the session scope
// names (see risk.go's classifyFootprint doc on the >=2-repositories
// absent-constant).
func singleRepository() []string {
	return []string{"session-repository"}
}

// probeRootOf resolves the risk classifier's content-probe root from
// SessionScope. An unresolved repository (Kind ScopeKindGeneral, no
// RepositoryRecord) yields an empty root, which readProbeFile treats as
// "skip content probes" rather than an error.
func probeRootOf(sc scope.SessionScope) string {
	if sc.Repository == nil {
		return ""
	}
	return sc.Repository.RootPath
}
