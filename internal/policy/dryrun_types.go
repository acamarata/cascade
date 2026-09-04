// Package policy (dryrun_types.go): Purpose: the value vocabulary of the
//
//	dry-run simulator — what a caller asks about (DryRunInput) and what it
//	is told would happen (DryRunResult), plus the subject-scoped grant
//	reference a report carries.
//
// Inputs: none; this file is declarations plus total functions over its
//
//	own values.
//
// Outputs: DryRunInput, DryRunResult, GrantRef.
//
// Constraints: a report is a PREDICTION, and its whole value is that it
//
//	matches what the live path would do, so DryRunInput carries the real
//	EvalRequest rather than a parallel input model: there is exactly one
//	request shape in this package, and a field added to the live one cannot
//	be forgotten here. DryRunResult is FAIL CLOSED on the same terms as
//	every other decision shape in the package — its zero value reads as a
//	deny at L4, because a report whose fields were never filled must not
//	read as permission. Nothing in this file is a map, and nothing renders
//	one: a report is compared and diffed by callers, so identical inputs
//	must produce byte-identical output and Go's map iteration order is not
//	an ordering at all.
//
// SPORT: internal/policy DryRunInput/ADDED, DryRunResult/ADDED,
//
//	GrantRef/ADDED (P1-E09-W2-S18-T4).
package policy

import (
	"time"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// DryRunInput is one prospective action a caller wants a prediction for.
//
// It holds the live EvalRequest whole. That is deliberate: a simulator that
// accepted its own flattened copy of the request would be a second input
// model, and the first field the live request gained without a matching
// field here would silently stop being simulated.
type DryRunInput struct {
	// Request is the action to predict, in exactly the shape
	// Engine.Evaluate takes.
	Request EvalRequest
	// SensitivityOverride names the tier the caller wants the simulation
	// resolved under (§5.16). It is OPTIONAL in the sense that it may be
	// left unset, not in the sense that leaving it unset is permissive:
	// an unset or unrecognised value is an UNRESOLVABLE tier and resolves
	// to the most restricted reach this package has,
	// corpus.VisibilityPrivate. There is no value of this field, present
	// or absent, that widens the reported reach.
	SensitivityOverride corpus.VisibilityClass
}

// GrantRef names one standing grant a report may mention. It is a
// deliberately thin projection of Grant: a report renders the grant's
// reach and lifetime, never its conditions, because a condition map would
// both leak the shape of the operator's policy into a prediction and
// reintroduce map iteration into rendered output.
//
// Every GrantRef in a report belongs to the SUBJECT THAT WAS SIMULATED. A
// report never names a grant held by anybody else, so a simulation cannot
// be used to enumerate another principal's entitlements.
type GrantRef struct {
	// Capability is the registered capability the grant is held on.
	Capability string `json:"capability"`
	// ScopeClass is the grant's own reach ceiling.
	ScopeClass corpus.VisibilityClass `json:"scope_class"`
	// ExpiresAt is when the grant stops applying; zero means it does not
	// expire on its own.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// Conditional reports that the grant carries conditions the request's
	// attributes must satisfy. The conditions themselves are not rendered.
	Conditional bool `json:"conditional"`
	// Matched reports that this grant is the one that decided, i.e. the
	// simulated outcome came from the standing-grant layer.
	Matched bool `json:"matched"`
}

// DryRunResult is what WOULD happen, in the vocabulary R-14.27 fixed: one
// canonical Verdict, with elevation as a separate flag rather than a
// fourth verdict value.
//
// The zero value denies. Verdict's zero is not a member of the enum and
// safeVerdict maps it to deny, RiskLevel's zero maps to L4, and every
// constructor below fills both explicitly, so no path can hand back a
// report that reads as allow because it was never populated.
type DryRunResult struct {
	// Verdict is the decision the live path would reach.
	Verdict Verdict `json:"verdict"`
	// ElevationRequired reports that the action's verb is elevation-class
	// (§5.14) and must be authorized at the machine it runs on. It is a
	// FLAG, never a verdict value (R-14.27), and it is derived from
	// internal/rpc's canonical table rather than from a second list.
	ElevationRequired bool `json:"elevation_required"`
	// RiskLevel is the rung actually evaluated, after the capability's
	// own class was folded in. It is never lower than the requested rung.
	RiskLevel RiskLevel `json:"risk_level"`
	// MatchedRule names the R-14.26 layer that decided, e.g.
	// "standing-grant". It is the layer's own stable name.
	MatchedRule string `json:"matched_rule"`
	// ApplicableGrants are the simulated subject's standing grants on the
	// requested capability, with the deciding one marked.
	ApplicableGrants []GrantRef `json:"applicable_grants,omitempty"`
	// Explanation is the engine's own reason string for the decision.
	Explanation string `json:"explanation"`
	// AutoAdvance reports whether an autonomous loop could proceed past
	// this action without a human turn.
	AutoAdvance bool `json:"auto_advance"`
	// EffectiveScope is how far this action's material could travel once
	// the resolved sensitivity tier is applied to the grant's own reach:
	// the narrower of the two, computed through the same choke point the
	// live carrier uses.
	EffectiveScope corpus.VisibilityClass `json:"effective_scope"`
	// WouldEmitAudit reports whether the live path would have written an
	// audit row for this action. It is informational: this run wrote
	// none.
	WouldEmitAudit bool `json:"would_emit_audit"`
}

// deniedSimulation builds the refusal shape every terminal simulation path
// returns, so no path can hand back a zero DryRunResult whose Verdict and
// RiskLevel would have to be interpreted rather than read.
func deniedSimulation(level RiskLevel, reason string) DryRunResult {
	return DryRunResult{
		Verdict:        VerdictDeny,
		RiskLevel:      safeLevel(level),
		MatchedRule:    LayerFailClosed.String(),
		Explanation:    reason,
		EffectiveScope: corpus.VisibilityPrivate,
	}
}

// resolveSensitivity applies §5.16's fail-closed rule to the caller's
// requested tier: a tier this package cannot recognise is unresolvable,
// and an unresolvable tier is the restricted one. It is a total function
// with no permissive branch.
func resolveSensitivity(v corpus.VisibilityClass) corpus.VisibilityClass {
	if !v.Valid() {
		return corpus.VisibilityPrivate
	}
	return v
}
