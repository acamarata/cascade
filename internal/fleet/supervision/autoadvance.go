// Purpose: the tier-1 auto-advance ceiling — the ask-resolution stage that
//
//	may turn a policy `ask` into an approval, and nothing else.
//
// WHERE IT RUNS, AND WHY THAT MATTERS. Inside the ONE authorization
//
//	middleware (internal/events/routing's RouteAction), after the policy
//	engine has decided. It is not a second decision point and not a
//	hook-layer bypass: R-21.207 makes policy.Authorize the single
//	authorization path for every action origin, hooks included, and a
//	second gate that could independently permit something would make that
//	claim false.
//
// IT CAN ONLY NARROW. The engine's verdict is the ceiling on this one:
//
//	`allow` stays allow, `deny` stays deny, and only `ask` is eligible to
//	resolve upward — and then only when all four conditions hold at once.
//
// Inputs: the action, the engine's decision, and the instruction source's
//
//	trust tag. The risk level and the per-level profile ceiling arrive
//	inside the decision; they are not recomputed here (R-21.236 resolves
//	the rung exactly once).
//
// Outputs: one Verdict.
// Constraints: fail closed on every unreadable input, and on panic. The
//
//	evaluator holds no state and touches no I/O, so it is safe to call
//	concurrently from every action origin.
//
// SPORT: internal/fleet/supervision:autoadvance (ADD) — P1-E18-W4-S39-T2.

package supervision

import (
	"context"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// CeilingReader reports the running profile's master switch. It is a seam
// rather than a value so a profile swap (autonomy_controller.go replaces
// the pointer) is observed on the next action, not at construction.
type CeilingReader interface {
	// AutoAdvanceCeiling returns the ceiling currently in force.
	AutoAdvanceCeiling() policy.Ceiling
}

// AutoAdvanceEvaluator decides whether an `ask` may resolve to an
// approval without a human turn.
type AutoAdvanceEvaluator struct {
	ceiling CeilingReader
}

// NewAutoAdvanceEvaluator builds an evaluator over ceiling.
//
// A nil reader is permitted and means "no profile is readable", which
// every call then resolves as profile_disabled. That is deliberate: a
// daemon whose profile source has not been wired must run with the feature
// OFF, not refuse to start — and it must not be able to auto-approve.
func NewAutoAdvanceEvaluator(ceiling CeilingReader) *AutoAdvanceEvaluator {
	return &AutoAdvanceEvaluator{ceiling: ceiling}
}

// Evaluate applies the four conditions.
//
// The order is chosen so the verdict names the FIRST thing an operator
// would have to change, and so the cheapest refusals come first:
//
//  1. elevated verb — never auto-approvable at any level, by any profile;
//  2. untrusted source — never auto-approvable, whatever the level;
//  3. profile master switch — the operator has not opted in;
//  4. risk ceiling — the engine's own per-level result.
//
// A panic anywhere inside becomes fail_closed_denied and never escapes:
// this runs inside the authorization middleware, and a panic that escaped
// would take down the decision path for every action origin at once.
func (e *AutoAdvanceEvaluator) Evaluate(
	ctx context.Context,
	action ActionDescriptor,
	decision PolicyDecision,
	trust TrustTag,
) (verdict Verdict, err error) {
	defer func() {
		if r := recover(); r != nil {
			verdict, err = VerdictFailClosedDenied, errEvaluatorPanicked(r)
		}
	}()
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return VerdictFailClosedDenied, ctxErr
		}
	}
	return e.decide(action, decision, trust), nil
}

// decide is Evaluate without the recover, so the conditions read as one
// list rather than being buried under the guard.
func (e *AutoAdvanceEvaluator) decide(
	action ActionDescriptor, decision PolicyDecision, trust TrustTag,
) Verdict {
	if !decision.Level.Valid() {
		// An unreadable rung is treated as the top of the ladder, exactly
		// as §5.15 requires — never as "probably fine".
		return VerdictFailClosedDenied
	}
	if action.Elevated {
		return VerdictElevatedRefused
	}
	if trust != corpus.TrustTrusted {
		// Includes an unset or unrecognised tag: anything that is not
		// positively trusted is untrusted here.
		return VerdictUntrustedRefused
	}
	if e.ceiling == nil || !e.ceiling.AutoAdvanceCeiling().AllowsTier1() {
		return VerdictProfileDisabled
	}
	if !decision.AutoAdvance {
		// The engine's own per-level ceiling said no. This covers both
		// "the level is above L1" and "this profile's slot forbids it" —
		// one answer, computed once, in the layer that owns it.
		return VerdictCeilingRefused
	}
	return VerdictAutoApproved
}

// Resolve applies the evaluator to one engine decision and reports the
// verdict alongside the verdict the caller should act on.
//
// This is the whole integration surface: a caller hands in what the engine
// decided and gets back what to do. Only an `ask` is eligible to change,
// and it can only become `allow` — every other input verdict is returned
// untouched, which is what makes "can only narrow" checkable in one place
// rather than at every call site.
func (e *AutoAdvanceEvaluator) Resolve(
	ctx context.Context, action ActionDescriptor, decision PolicyDecision, trust TrustTag,
) (policy.Verdict, Verdict, error) {
	if decision.Verdict != policy.VerdictAsk {
		return decision.Verdict, VerdictCeilingRefused, nil
	}
	verdict, err := e.Evaluate(ctx, action, decision, trust)
	if err != nil || !verdict.Approved() {
		return policy.VerdictAsk, verdict, err
	}
	return policy.VerdictAllow, verdict, nil
}
