package policy

// Purpose: how one request's rung is chosen. Split from evaluator.go for
//   Art.10.3's 300-line cap; the decision it encodes is small but load
//   bearing, and it reads better next to its own reasoning than buried in
//   the evaluation entry point.
// Inputs: the request and the capability the registry resolved for it.
// Outputs: the classified rung, before the capability's own class is
//   applied as a floor by the caller.
// Constraints: only a request that DECLARES itself command-less takes its
//   rung from the capability. Text classifies as text, and text the
//   classifier does not recognise is L4.
// SPORT: internal.policy.resolveRung/ADDED (T0, R-14.211).

import "context"

// resolveRung produces the classified rung for one request.
//
// An action WITH command text is classified from that text, and text the
// classifier does not recognise is L4, which is correct: refusing to run an
// unrecognised COMMAND at the top rung is the whole point of the ladder.
//
// An action that DECLARES itself command-less is different in kind, not
// in degree, and it must say so explicitly: an empty Action alone is still
// unclassifiable and still L4, because a hook that lost its command is
// malformed rather than command-less. An
// in-process scheduled dispatch has no command line to read, and the
// scheduler previously fabricated a command-shaped string to fill this
// field. The classifier then correctly judged that synthetic string
// unrecognisable, producing L4, which layer 1 treats as an unconditional
// deny-list hit and which hardVerdictFloor clamps below any standing
// grant. The result was an action nothing could authorize, including the
// operator gesture scheduler_route.go documents as the intended one
// (R-14.211).
//
// So when there is no text, the rung comes from the capability's own
// declared class, which is exactly what Capability.DefaultPolicy is
// documented to be: the class this capability confers when no narrower
// rule applies. Nothing is invented and nothing is lowered. The caller
// still cannot reach this path by supplying a command the classifier does
// not recognise, because that is text, and text classifies as text.
func (e *Engine) resolveRung(ctx context.Context, req EvalRequest, capDef Capability) RiskLevel {
	if req.CommandLess {
		return capDef.Class().Risk()
	}
	return e.classify(ctx, req.Action)
}
