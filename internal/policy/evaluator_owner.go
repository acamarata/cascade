// Package policy (evaluator_owner.go): Purpose: the owner consult step of
//
//	the evaluation order (P2-GOV-84), plus its audit obligation
//	(P2-GOV-85). A live owner grant may widen an ask-tier decision into
//	allow, but only if that widening is accounted for: exactly one audit
//	record naming the grant id, the subject, the action and the rung.
// Inputs: the base profile verdict, the resolved rung, the deciding
//	Subject, the action name, the redeemed grant id and an injected
//	audit.Writer.
// Outputs: the effective Verdict — allow only when the widening was both
//	authorised and recorded.
// Constraints: fail closed. A decision the base profile already allowed
//	is returned untouched and writes no grant record; a widened decision
//	whose audit write fails falls back to the base verdict rather than
//	proceeding unaudited. No clock read, no randomness, no payload.
// SPORT: internal.policy.consultOwnerGrant/ADDED (P2-GOV-85).

package policy

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// consultOwnerGrant resolves the effective verdict for one decision under
// a live owner grant. base is what the autonomy table decided on its own;
// lvl is the resolved rung. When base already allows, the grant changed
// nothing and no record is written. Otherwise the grant widens ask into
// allow only after auditOwnerGrant has sealed the record accounting for it:
// any write error returns base unchanged, so an unaccountable decision is
// never allowed.
func consultOwnerGrant(ctx context.Context, w audit.Writer, grantID cascade.ID, subject Subject, action string, lvl RiskLevel, base Verdict) Verdict {
	if safeVerdict(base) == VerdictAllow {
		return base
	}
	if grantID == "" || safeVerdict(base) != VerdictAsk {
		return base
	}
	if err := auditOwnerGrant(ctx, w, grantID, subject, action, lvl.String()); err != nil {
		return base
	}
	return VerdictAllow
}
