// Package routing is the hook and scheduler call site of the single
// authorization middleware. It holds ActionRouter, which every hook-fired
// shell action and every cron-triggered dispatch passes through before it
// runs.
//
// Purpose: gate an action on the ONE policy evaluation entry point and
//
//	record the routing decision in the audit log.
//
// Inputs: an Action (who, which capability, what text, which origin) and
//
//	the injected policy engine and audit writer.
//
// Outputs: the verdict, the trace the evaluation produced, and a non-nil
//
//	error whenever the decision could not be made or could not be
//	recorded. A deny is a normal outcome and carries a typed error the
//	caller surfaces; it is never a silent pass.
//
// # The router classifies nothing
//
// ActionRouter holds a policy engine and nothing else. It resolves no risk
// level, applies no wrapper or indirection rule and handles no classifier
// error: classification lives solely inside the evaluator, which resolves
// the rung exactly once per evaluation (R-21.236). RouteAction calls the
// one Evaluate signature this tree has and reads its outcome; it adds no
// decision of its own.
//
// # The seven layers
//
// The evaluator this package calls applies, in this order:
//
//	layer 0 — data-class check (UNCONDITIONAL, terminal ErrDataClassDenied)
//	layer 1 — deny-list
//	layer 2 — elevation check
//	layer 3 — standing grants
//	layer 4 — capability default policy
//	layer 5 — autonomy profile
//	layer 6 — fail-closed fallback
//
// Layer 0 is not part of first-match-wins and no later layer can release
// it. Layer 6 is what a request nothing above resolved decides on, so the
// absence of a rule is a refusal rather than a pass.
//
// # Fail closed
//
// Every path that cannot produce a verdict returns deny with an error: a
// nil router, a nil engine, a nil context, an action that does not
// validate, an evaluation that errors, and a verdict outside the three
// defined values. An audit write that fails also denies, because an
// authorization decision nothing recorded is one nobody can review.
//
// SPORT: internal.events.routing.ActionRouter/ADDED (P1-E09-W2-S18-T5).
package routing

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Origin names the action origin a routed call came from. Every origin
// reaches the policy engine through this one middleware; an origin this
// set does not name is refused rather than defaulted (R-21.207).
type Origin string

const (
	// OriginHook is a hook-fired action.
	OriginHook Origin = "hook"
	// OriginScheduler is a cron-triggered scheduled dispatch.
	OriginScheduler Origin = "scheduler"
)

// Valid reports whether o is one of the defined origins. The empty Origin
// is not one of them.
func (o Origin) Valid() bool { return o == OriginHook || o == OriginScheduler }

// RouteDeniedCode is the stable, greppable identifier every routing
// refusal carries in its message, so a caller, a test or an audit reader
// can name this refusal class without a new taxonomy Kind (the fourteen
// are frozen).
const RouteDeniedCode = "ACTION_ROUTE_DENIED"

// Action is one action awaiting a routing decision. It is the vocabulary
// the hook dispatcher and the scheduler both speak; the router translates
// it into the evaluator's request and nothing else.
type Action struct {
	// Subject is who is acting.
	Subject policy.Subject
	// Capability is the registered capability the action needs. An
	// unregistered name denies inside the evaluator.
	Capability string
	// Verb is the method name layer 2 classifies for elevation.
	Verb string
	// Command is the canonical text of what would run. It is the
	// classifier's only input and the router never inspects it.
	Command string
	// Params are the action's parameters, hashed into the audit row and
	// bound to any approval the evaluation files.
	Params []byte
	// Origin names which call site routed this action.
	Origin Origin
	// Ref identifies the originating hook or job, for the audit row.
	Ref string
	// Summary is the human-readable line an approval prompt shows.
	Summary string
}

// Validate refuses an action the router cannot route. Every field it names
// is required: an action missing one could only be evaluated by assuming
// something nobody wrote, and every such assumption is a widening.
func (a Action) Validate() error {
	if !a.Origin.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %q is not an action origin (%s)", string(a.Origin), RouteDeniedCode)
	}
	if a.Ref == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %s action has no ref (%s)", a.Origin, RouteDeniedCode)
	}
	if a.Capability == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %s action %q names no capability (%s)", a.Origin, a.Ref, RouteDeniedCode)
	}
	if a.Command == "" {
		return cascade.Newf(cascade.KindInvalidInput,
			"routing: %s action %q has no command text to classify (%s)",
			a.Origin, a.Ref, RouteDeniedCode)
	}
	return a.Subject.Validate()
}

// PolicyEngine is the evaluation seam. It is satisfied by *policy.Engine
// and declared as an interface so a call site can be exercised against a
// deliberately failing evaluator without reaching into the engine.
type PolicyEngine interface {
	// Evaluate decides one action. Its signature is the evaluator's own,
	// verbatim: no caller supplies a risk level and no caller receives a
	// bare verdict.
	Evaluate(ctx context.Context, req policy.EvalRequest) (policy.EvalOutcome, error)
}

// Compile-time proof that the production engine satisfies the seam its
// call sites inject.
var _ PolicyEngine = (*policy.Engine)(nil)

// ActionRouter is the hook and scheduler call site of the one
// authorization middleware. The zero value is not usable; construct with
// NewActionRouter.
type ActionRouter struct {
	engine PolicyEngine
	audit  audit.Writer
}

// NewActionRouter returns a router over engine, recording every decision
// through auditor. Both are required: a router with no engine could only
// answer by assuming, and a router with no audit writer would decide
// without leaving a record.
func NewActionRouter(engine PolicyEngine, auditor audit.Writer) (*ActionRouter, error) {
	if engine == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "routing: action router requires a policy engine")
	}
	if auditor == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "routing: action router requires an audit writer")
	}
	return &ActionRouter{engine: engine, audit: auditor}, nil
}

// RouteAction decides whether action may run.
//
// It returns the verdict, the trace the evaluation produced, and an error.
// A deny is a normal outcome: it is returned as a verdict together with a
// typed error the caller surfaces, never as a nil error the caller might
// read as permission. Every failure to decide returns deny.
func (r *ActionRouter) RouteAction(ctx context.Context, action Action) (policy.Verdict, policy.Trace, error) {
	if r == nil || r.engine == nil {
		return policy.VerdictDeny, policy.Trace{},
			cascade.Newf(cascade.KindPolicyDenied, "routing: no policy engine to route through (%s)", RouteDeniedCode)
	}
	if ctx == nil {
		return policy.VerdictDeny, policy.Trace{},
			cascade.Newf(cascade.KindInvalidInput, "routing: nil context (%s)", RouteDeniedCode)
	}
	if err := action.Validate(); err != nil {
		return policy.VerdictDeny, policy.Trace{}, err
	}

	out, evalErr := r.engine.Evaluate(ctx, action.request())
	verdict, err := decide(action, out, evalErr)
	if auditErr := r.record(ctx, action, out, verdict); auditErr != nil {
		return policy.VerdictDeny, out.Trace, auditErr
	}
	return verdict, out.Trace, err
}

// decide folds the evaluation's result into the verdict the caller acts
// on. An evaluation that errored, and a verdict outside the three defined
// values, both become deny: there is no path by which an unreadable answer
// reads as permission.
func decide(action Action, out policy.EvalOutcome, evalErr error) (policy.Verdict, error) {
	if evalErr != nil {
		return policy.VerdictDeny, cascade.Wrapf(cascade.KindPolicyDenied, evalErr,
			"routing: %s action %q could not be evaluated (%s)", action.Origin, action.Ref, RouteDeniedCode)
	}
	switch out.Verdict {
	case policy.VerdictAllow, policy.VerdictAsk:
		return out.Verdict, nil
	case policy.VerdictDeny:
		return policy.VerdictDeny, cascade.Newf(cascade.KindPolicyDenied,
			"routing: %s action %q refused at the %s layer: %s (%s)",
			action.Origin, action.Ref, out.Layer, out.Reason, RouteDeniedCode)
	default:
		return policy.VerdictDeny, cascade.Newf(cascade.KindPolicyDenied,
			"routing: %s action %q produced no readable verdict (%s)",
			action.Origin, action.Ref, RouteDeniedCode)
	}
}

// request builds the evaluator's request from action. It supplies no risk
// level, because no caller of Evaluate does.
func (a Action) request() policy.EvalRequest {
	return policy.EvalRequest{
		Subject:    a.Subject,
		Capability: a.Capability,
		Verb:       a.Verb,
		Action:     a.Command,
		Params:     a.Params,
		Summary:    a.Summary,
		Attributes: map[string]string{"origin": string(a.Origin), "ref": a.Ref},
	}
}

// record appends the policy.route row for this decision. A failed append
// is returned to the caller, which turns the routing call into a deny: an
// action nobody could record having authorized does not run.
func (r *ActionRouter) record(ctx context.Context, action Action,
	out policy.EvalOutcome, verdict policy.Verdict) error {
	_, err := r.audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      action.Subject.String(),
		Action:     string(action.Origin) + ":" + action.Ref,
		ParamsHash: audit.HashParams(action.Params),
		RiskLevel:  out.Level.String(),
		Verdict:    verdict.String(),
		Outcome:    out.Layer.String(),
	})
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err,
			"routing: %s action %q decision could not be recorded (%s)",
			action.Origin, action.Ref, RouteDeniedCode)
	}
	return nil
}
