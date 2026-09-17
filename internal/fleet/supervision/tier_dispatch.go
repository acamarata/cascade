// Purpose: the ONE place the configured supervision tier decides what
//
//	happens to an ask — so the router keeps exactly one seam for
//	supervision rather than three, and the tier logic lives in the package
//	that owns the tiers.
//
// WHERE IT SITS. Immediately after the engine's single Evaluate, on the
//
//	ask-resolution path the router already owns (R-14.247 companion
//	finding). The contract named a "governor session lifecycle"; there is
//	no such thing in this tree — internal/fleet/governor is the ADMISSION
//	controller — and the real point after AutoAdvanceEvaluator.Evaluate is
//	routing.ActionRouter.resolveAutoAdvance, which is already the one
//	authorization middleware (R-21.207).
//
// WHAT IT MAY NOT DO. It may not resolve the rung a second time
//
//	(R-21.236). It is handed the level the engine already resolved and
//	never looks at the command text to form its own view.
//
// Inputs: a LIVE tier read, so a `[fleet.supervision].tier` reload is
//
//	observed by the next action rather than at the next daemon restart,
//	and the two higher-tier supervisors.
//
// Outputs: handled=false means "tier 1 — let the auto-advance stage
//
//	decide", which is exactly what the router did before this existed.
//
// SPORT: fleet.supervision.TierDispatcher/ADDED (P1-E18-W4-S39-T3).

package supervision

import (
	"context"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TierReader reports the tier currently in force. It is a function rather
// than a stored value for the same reason the ceiling is read live: an
// operator who lowers the tier in the config file has changed what the
// NEXT action gets, not what the next restart gets.
type TierReader func() Tier

// TierDispatcher routes an ask to the supervisor the configured tier
// names.
type TierDispatcher struct {
	tier TierReader
	t2   *Tier2Supervisor
	t3   *Tier3Supervisor
}

// NewTierDispatcher builds the dispatcher.
//
// The tier reader is required. The two supervisors are not: a daemon
// configured for tier 1 has no reason to construct either, and requiring
// them would make the common configuration pay for the uncommon ones. A
// tier selected with no supervisor behind it is a REFUSAL at dispatch
// time, not a silent fall back to tier 1 — falling back would quietly give
// an operator less supervision than they asked for, which is the single
// worst direction for this particular mistake.
func NewTierDispatcher(tier TierReader, t2 *Tier2Supervisor, t3 *Tier3Supervisor) (*TierDispatcher, error) {
	if tier == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: the tier dispatcher requires a live tier reader")
	}
	return &TierDispatcher{tier: tier, t2: t2, t3: t3}, nil
}

// Supervise resolves an ask under the configured tier.
//
// handled=false means tier 1: the caller's own auto-advance stage decides,
// unchanged. handled=true means this tier answered, and the returned
// verdict and error are the answer.
func (d *TierDispatcher) Supervise(
	ctx context.Context, req policy.EvalRequest, out policy.EvalOutcome,
) (handled bool, verdict policy.Verdict, err error) {
	switch d.tier() {
	case TierHookMediated:
		return false, policy.VerdictAsk, nil
	case TierPTYAttached:
		v, serr := d.superviseTier2(ctx, req, out)
		return true, v, serr
	case TierSuggestOnly:
		v, serr := d.superviseTier3(ctx, req, out)
		return true, v, serr
	default:
		// A tier nobody chose. The config parser rejects an unrecognised
		// value, so reaching this means the reader itself returned
		// something invalid, and the safe answer is the ask the engine
		// already reached — never an approval.
		return true, policy.VerdictDeny, cascade.Newf(cascade.KindInvalidInput,
			"supervision: %q is not a configured tier", d.tier().String())
	}
}

// superviseTier2 attaches a terminal and asks.
//
// The attach happens per HELD action rather than once per session: an L0
// read must not pay for a terminal, and a terminal held open across an
// idle daemon is a resource nobody is using. Below the held rung the tier
// answers without attaching anything at all.
func (d *TierDispatcher) superviseTier2(
	ctx context.Context, req policy.EvalRequest, out policy.EvalOutcome,
) (policy.Verdict, error) {
	if d.t2 == nil {
		return policy.VerdictDeny, cascade.New(cascade.KindUnavailable,
			"supervision: tier 2 is configured but no PTY supervisor is wired; refusing rather than "+
				"silently supervising less than the operator asked for")
	}
	if !d.t2.Holds(out.Level) {
		// Below the held rung tier 2 has no opinion, so the ask falls
		// through to the auto-advance stage exactly as under tier 1.
		return policy.VerdictAsk, nil
	}
	sess, err := d.t2.Run(ctx)
	if err != nil {
		return policy.VerdictDeny, err
	}
	defer func() { _ = sess.Close() }()
	if err := d.t2.Approve(ctx, sess, req, out); err != nil {
		return policy.VerdictDeny, err
	}
	return policy.VerdictAllow, nil
}

// superviseTier3 files a suggestion and refuses to execute.
//
// The refusal is KindPolicyDenied and names the tier, because an agent
// that is told only "denied" retries, while one told the action is waiting
// for a person does not.
func (d *TierDispatcher) superviseTier3(
	ctx context.Context, req policy.EvalRequest, out policy.EvalOutcome,
) (policy.Verdict, error) {
	if d.t3 == nil {
		return policy.VerdictDeny, cascade.New(cascade.KindUnavailable,
			"supervision: tier 3 is configured but no suggest-only supervisor is wired")
	}
	sg, err := d.t3.Suggest(ctx, req, out)
	if err != nil {
		return policy.VerdictDeny, err
	}
	return policy.VerdictDeny, cascade.Newf(cascade.KindPolicyDenied,
		"supervision: tier 3 is suggest-only; %q was recorded for review and NOT run", sg.Ref)
}
