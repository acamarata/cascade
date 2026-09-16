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

// resolveAutoAdvance runs the stage.
//
// Only an `ask` with no error is eligible. Everything else — allow, deny,
// or an evaluation that already failed — passes through untouched, which
// is what keeps this incapable of widening anything.
func (r *ActionRouter) resolveAutoAdvance(
	ctx context.Context, action Action, out policy.EvalOutcome, verdict policy.Verdict, err error,
) (policy.Verdict, error) {
	if r.advance == nil || verdict != policy.VerdictAsk || err != nil {
		return verdict, err
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
