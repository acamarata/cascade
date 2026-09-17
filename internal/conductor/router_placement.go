package conductor

// Purpose: the K×Q seam — the node-ELIGIBILITY consult the router runs
//   before it scores a single lane, when the request demands capabilities
//   of the MACHINE rather than of the model lane.
// Inputs: a provider.ModelRequest (its Requirements.NodeCapabilities,
//   Sensitivity and Policy), plus the enrolled fleet and the placement
//   engine wired at the composition root.
// Outputs: the reason flags the consult contributed, or the placement
//   engine's own typed refusal.
// Constraints: the router owns lane SCORING; internal/nodes owns node
//   ELIGIBILITY (P1-E17-W4-S37-T1). This file adds no second copy of the
//   eligibility rules — it supplies inputs and propagates the answer. A
//   request that names node capabilities with no placement engine wired
//   REFUSES; silently routing it as though it had asked for nothing is
//   precisely the defect this seam exists to end.
// SPORT: conductor.router node-eligibility seam (CHG) — P1-E17-W4-S37-T1.

import (
	"strconv"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// NodeEligibility is the consumer-side seam the router calls. Its one
// method is transcribed character-for-character from internal/nodes'
// Engine.Eligible — not paraphrased from a description of it — and the
// compile-time assertion below is what keeps the two in step: a change to
// the real method breaks this build rather than drifting silently.
type NodeEligibility interface {
	Eligible(req nodes.Requirement, candidates []nodes.Candidate) ([]nodes.DeviceRecord, error)
}

var _ NodeEligibility = nodes.Engine{}

// NodeFleet supplies the enrolled records the consult decides over. Its
// one method is transcribed from internal/nodes' RecordStore.List, which
// the assertion below pins.
type NodeFleet interface {
	List() ([]nodes.DeviceRecord, error)
}

var _ NodeFleet = (*nodes.RecordStore)(nil)

// WithNodePlacement wires the K×Q seam and returns r, so a composition
// root can chain it onto NewRouter/NewDaemonRouter.
//
// Passing a nil engine or fleet leaves the router in its unwired state,
// which is NOT the same as "no placement rule": see consultPlacement for
// what an unwired router does with a request that names node
// capabilities.
func (r *DefaultRouter) WithNodePlacement(engine NodeEligibility, fleet NodeFleet) *DefaultRouter {
	if engine == nil || fleet == nil {
		return r
	}
	r.placement, r.fleet = engine, fleet
	return r
}

// ErrNodePlacementUnavailable is the refusal a request naming node
// capabilities gets from a router with no placement engine wired.
//
// KindUnavailable, not KindInvalidInput: the request is well formed and
// the capability names may be perfectly real. This build simply cannot
// decide where to run it, and the alternative — routing it to an ordinary
// lane as though `--require node.browser=true` had not been typed — is the
// silent drop P1-E17-W4-S37-T1 was opened to fix.
var ErrNodePlacementUnavailable = cascade.New(cascade.KindUnavailable,
	"conductor: this request requires node capabilities but no node-placement engine is wired")

// consultPlacement runs the eligibility step and reports the flags it
// contributed. It is the FIRST thing SelectExplain does, ahead of the
// registry snapshot: a request that cannot be placed anywhere should not
// cost a registry read, and eligibility is the more fundamental question
// ("is there a machine for this at all") than lane scoring ("which lane").
//
// A request with no node capabilities is not a placement question, so the
// consult does nothing and contributes no flag — which is what keeps every
// ordinary model call on exactly the path it took before this seam
// existed.
func (r *DefaultRouter) consultPlacement(req provider.ModelRequest, flags []string) ([]string, error) {
	required := req.Requirements.NodeCapabilities
	if len(required) == 0 {
		return flags, nil
	}
	if r.placement == nil || r.fleet == nil {
		return append(flags, "placement:no-engine-wired"), ErrNodePlacementUnavailable
	}
	records, err := r.fleet.List()
	if err != nil {
		return append(flags, "placement:fleet-unreadable"), err
	}
	eligible, err := r.placement.Eligible(
		nodes.Requirement{Capabilities: required, Sensitivity: placementSensitivity(req)},
		nodes.CandidatesFrom(records),
	)
	if err != nil {
		return append(flags, "placement:no-eligible-node"), err
	}
	return append(flags, "placement:eligible-nodes="+strconv.Itoa(len(eligible))), nil
}

// placementSensitivity maps the request's routing classification onto the
// placement engine's own vocabulary.
//
// Policy.ExternalAllowed is read FIRST and is decisive when false, because
// pkg/provider.Policy says so in as many words: false "forces local-only
// placement regardless of Sensitivity". A caller that both forbids leaving
// the controller machine and demands a capability only another machine can
// advertise has asked for two incompatible things, and local-only is the
// half that fails closed.
//
// Every tier this build does not recognize maps to local-only for the same
// reason internal/nodes' ResolveSensitivity does: an unresolvable
// classification is the case where guessing wrong leaks work off the
// controller machine.
func placementSensitivity(req provider.ModelRequest) nodes.Sensitivity {
	if !req.Policy.ExternalAllowed {
		return nodes.SensitivityLocalOnly
	}
	switch req.Sensitivity {
	case provider.SensitivityLocalOnly:
		return nodes.SensitivityLocalOnly
	case provider.SensitivityRestricted:
		return nodes.SensitivityRestricted
	case provider.SensitivityInternal, provider.SensitivityPublic:
		return nodes.SensitivityNormal
	default:
		return nodes.SensitivityLocalOnly
	}
}

// RouterOption is one optional wiring step a composition root applies to a
// freshly built DefaultRouter.
//
// It exists so internal/daemon's RegisterConductorRouter can gain optional
// collaborators without every caller — its tests included — having to name
// the ones it does not have (R-16.79's "smallest real change, not a
// signature ripple"), and so this package keeps deciding what wiring a
// router accepts rather than exporting its fields.
type RouterOption func(*DefaultRouter)

// NodePlacement is the RouterOption that wires the K×Q node-eligibility
// seam. It is WithNodePlacement in option form and applies exactly the
// same nil handling.
func NodePlacement(engine NodeEligibility, fleet NodeFleet) RouterOption {
	return func(r *DefaultRouter) { r.WithNodePlacement(engine, fleet) }
}
