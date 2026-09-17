// Purpose: the tier-1 ask-resolution stage inside the ONE authorization
//
//	middleware — where a policy `ask` may become an allow without a human
//	turn, and where a refusal reaches a human instead.
//
// WHY IT LIVES IN THE ROUTER. R-21.207 makes policy.Authorize the single
//
//	authorization path for every action origin, hooks included. Putting
//	auto-advance anywhere else — a hook-layer shortcut, a second gate at a
//	call site — would create a way to permit an action that did not pass
//	through the middleware, and that would make the single-path claim
//	false. It runs AFTER the engine, so it can only ever narrow the set of
//	things that proceed unattended relative to what the engine allowed.
//
// Inputs: the action, the engine's outcome, and the verdict decide()
//
//	produced.
//
// Outputs: the verdict the caller acts on, and its error.
// Constraints: an unattached evaluator is a no-op — a daemon that has not
//
//	wired auto-advance behaves exactly as it did before this existed. A
//	failure inside the stage leaves the ask standing; it never denies an
//	action the engine was willing to ask about, and never allows one it
//	was not.
//
// SPORT: internal/events/routing:autoadvance (ADD) — P1-E18-W4-S39-T2.

package routing

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
)

// AutoAdvance is the ask-resolution seam the router consults. The concrete
// implementation is supervision.AutoAdvanceEvaluator; it is an interface
// here so the router does not gain that package's construction concerns.
type AutoAdvance interface {
	Resolve(ctx context.Context, action supervision.ActionDescriptor,
		decision supervision.PolicyDecision, trust supervision.TrustTag) (policy.Verdict, supervision.Verdict, error)
}

// AutoAdvanceSink receives the verdict for recording and for queueing a
// refusal. Both are optional in the sense that a nil sink skips them; the
// routing decision itself is audited by the router regardless.
type AutoAdvanceSink interface {
	Record(ctx context.Context, action supervision.ActionDescriptor,
		decision supervision.PolicyDecision, trust supervision.TrustTag, verdict supervision.Verdict) error
}

// DryRunFirst is the mandatory first-run dry-run gate the router consults
// before the ask-resolution stage may resolve an ask (R-14.247 §7). The
// concrete implementation is the supervision package's
// supervision.DryRunFirstGuard; it is an interface here for the same
// reason AutoAdvance is — the router does not gain the guard's
// construction concerns.
type DryRunFirst interface {
	// Guard receives the request the router ALREADY built for its one
	// live Evaluate, passed by value — never a copy re-derived from the
	// action descriptor, which carries no command text. A nil error
	// passes the stage; a non-nil one blocks the action.
	Guard(ctx context.Context, req policy.EvalRequest) error
}

// Compile-time proof that the production guard satisfies the seam.
var _ DryRunFirst = (*supervision.DryRunFirstGuard)(nil)

// TierSupervisor resolves an ask under the operator's configured
// supervision tier (P1-E18-W4-S39-T3, R-16.54). The concrete
// implementation is supervision.TierDispatcher.
//
// handled=false means tier 1 — the auto-advance stage below decides,
// exactly as it did before this seam existed. handled=true means the tier
// answered, and the router returns that answer unchanged: it does not
// second-guess a supervisor and it does not classify anything to reach it.
type TierSupervisor interface {
	Supervise(ctx context.Context, req policy.EvalRequest, out policy.EvalOutcome) (bool, policy.Verdict, error)
}

// Compile-time proof that the production dispatcher satisfies the seam.
var _ TierSupervisor = (*supervision.TierDispatcher)(nil)

// WithTierSupervision attaches the configured-tier stage.
//
// A router without one behaves exactly as before — tier 1 — which is also
// what the dispatcher answers when the config selects tier 1, so the two
// paths agree rather than merely coinciding.
func (r *ActionRouter) WithTierSupervision(tiers TierSupervisor) *ActionRouter {
	r.tiers = tiers
	return r
}

// WithAutoAdvance attaches the ask-resolution stage.
//
// It returns the router so a composition root can chain it, and it is a
// separate call rather than a constructor argument because most routers
// never have one: auto-advance is off by default, and a constructor
// parameter would make every call site state that it is off.
func (r *ActionRouter) WithAutoAdvance(advance AutoAdvance, sink AutoAdvanceSink) *ActionRouter {
	r.advance, r.advanceSink = advance, sink
	return r
}

// WithDryRunFirst attaches the mandatory first-run dry-run gate.
//
// It returns the router so a composition root can chain it. A router
// without a gate behaves exactly as before; the daemon's policy
// composition root always installs one. The gate is not configurable off
// and carries no switch — a bypassable first-run check would be a
// placeholder where a guarantee was specified.
func (r *ActionRouter) WithDryRunFirst(gate DryRunFirst) *ActionRouter {
	r.dryFirst = gate
	return r
}

// resolveAutoAdvance runs the stage.
//
// Only an `ask` with no error is eligible. Everything else — allow, deny,
// or an evaluation that already failed — passes through untouched, which
// is what keeps this incapable of widening anything.
func (r *ActionRouter) resolveAutoAdvance(
	ctx context.Context, action Action, req policy.EvalRequest,
	out policy.EvalOutcome, verdict policy.Verdict, err error,
) (policy.Verdict, error) {
	if r.advance == nil || verdict != policy.VerdictAsk || err != nil {
		return verdict, err
	}
	// The mandatory first-run dry-run gate (R-14.247 §7), before the
	// stage may turn an ask into an approval. It decides nothing: Simulate
	// discards its writes, and the verdict the caller acts on remains the
	// one this router's single Evaluate returned — the rung is still
	// resolved exactly once. A block is a deny with an error, so the ask
	// can never silently become an approval the first run was not
	// simulated for.
	if r.dryFirst != nil {
		if gateErr := r.dryFirst.Guard(ctx, req); gateErr != nil {
			return policy.VerdictDeny, gateErr
		}
	}
	// The configured supervision tier, after the dry-run gate and before
	// the tier-1 stage. Tiers 2 and 3 answer here and the stage below is
	// not consulted at all — an operator who asked for a human on the
	// terminal, or for suggestions only, did not ask for a hook to decide
	// as well. Tier 1 reports handled=false and changes nothing.
	if r.tiers != nil {
		handled, tierVerdict, tierErr := r.tiers.Supervise(ctx, req, out)
		if handled {
			return tierVerdict, tierErr
		}
	}
	descriptor := supervision.ActionDescriptor{
		Ref: action.Ref, Origin: string(action.Origin), Verb: action.Verb,
		Elevated: verbIsElevated(action.Verb),
	}
	decision := supervision.PolicyDecision{
		Verdict: verdict, AutoAdvance: out.AutoAdvance, Level: out.Level,
	}
	resolved, advanceVerdict, advanceErr := r.advance.Resolve(ctx, descriptor, decision, action.SourceTrust)
	r.recordAutoAdvance(ctx, descriptor, decision, action.SourceTrust, advanceVerdict)
	if advanceErr != nil {
		// The stage could not decide. The ask stands: an unreadable
		// auto-advance answer must not become either an approval or a
		// denial of something the engine was willing to ask about.
		return policy.VerdictAsk, err
	}
	return resolved, err
}

// recordAutoAdvance writes the verdict, ignoring a sink failure.
//
// A failed audit write here does NOT change the decision: the router has
// already written its own routing record unconditionally, so the decision
// is never unlogged, and failing an action because a supplementary record
// could not be written would trade a working system for a tidier log.
func (r *ActionRouter) recordAutoAdvance(
	ctx context.Context, descriptor supervision.ActionDescriptor,
	decision supervision.PolicyDecision, trust supervision.TrustTag, verdict supervision.Verdict,
) {
	if r.advanceSink == nil {
		return
	}
	_ = r.advanceSink.Record(ctx, descriptor, decision, trust, verdict)
}

// verbIsElevated reports whether verb needs the §5.14 elevation flow.
//
// An action with no verb is not a verb call and is not elevated. A verb
// this build does not recognise IS treated as elevated: an unregistered
// name is one nobody can prove is harmless, which is the same direction
// policy.LookupVerb itself resolves in.
func verbIsElevated(verb string) bool {
	if verb == "" {
		return false
	}
	spec, err := policy.LookupVerb(verb)
	if err != nil {
		return true
	}
	return spec.Elevated()
}
