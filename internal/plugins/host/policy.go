// Purpose: CheckGeneric, the fallback check for every host-ABI call that
//
//	is neither a net-scoped HTTP request nor a storage-domain access: it
//	delegates to the policy engine's own allow/ask/deny lookup and
//	explanation.
//
// Inputs: the capability string identifying the call (e.g.
//
//	"host.tool_register").
//
// Outputs: nil on an allow verdict; a denied, audited error — carrying
//
//	the engine's explanation — on ask, deny, or an evaluation error.
//
// Constraints: fail closed. A synchronous host call has no channel back
//
//	to an operator to answer an interactive approval, so PolicyVerdictAsk
//	is treated the same as PolicyVerdictDeny here — an ask a human never
//	sees is not a grant (see the ticket journal for why this reading was
//	chosen over blocking the plugin's call indefinitely).
//
// SPORT: internal/plugins/host policy-delegate-check (ADD) — P1-E15-W4-S31-T4.

package host

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PolicyVerdict is this package's own, string-free mirror of
// internal/policy.Verdict's three values (capability.go's doc comment
// explains why this package cannot depend on that type directly). The
// zero value is PolicyVerdictDeny, so a PolicyEngine implementation that
// forgets to set the field denies rather than allows.
type PolicyVerdict uint8

const (
	// PolicyVerdictDeny refuses the call outright. Also the zero value.
	PolicyVerdictDeny PolicyVerdict = iota
	// PolicyVerdictAllow permits the call.
	PolicyVerdictAllow
	// PolicyVerdictAsk would ask an operator to approve interactively.
	// CheckGeneric treats it as a denial (see this file's doc comment).
	PolicyVerdictAsk
)

// PolicyEngine is the seam onto the policy engine's grant lookup. A
// composition root outside internal/plugins/** adapts it to the real
// internal/policy.Engine.Evaluate plus policy.ExplainTrace (I/S-17.T2).
type PolicyEngine interface {
	// Evaluate returns the verdict for pluginID performing capability,
	// plus a human-readable explanation of why (the "explain-why" trace
	// text). explanation should be non-empty whenever err is nil, so a
	// denial's reason is never blank.
	Evaluate(ctx context.Context, pluginID, capability string) (verdict PolicyVerdict, explanation string, err error)
}

// genericCallType prefixes audit entries and denial messages for
// CheckGeneric so they read distinctly from the fixed http/storage/
// secret_ref call types, while still naming the capability itself.
const genericCallTypePrefix = "policy:"

// ErrNoPolicyEngine reports a CheckGeneric call on an enforcer built with
// no PolicyEngine. Missing infrastructure denies.
var ErrNoPolicyEngine = cascade.New(cascade.KindCapabilityDenied, "host: no policy engine configured")

// CheckGeneric evaluates capability through e's PolicyEngine and denies
// unless the verdict is PolicyVerdictAllow. A nil engine, an empty
// capability string, an evaluation error, and a PolicyVerdictAsk/Deny
// verdict all deny (fail closed); the denial's reason carries the
// engine's own explanation whenever one was produced, so an operator
// reading the audit trail sees WHY the policy engine decided as it did,
// not just that it did.
func (e *HostBoundaryEnforcer) CheckGeneric(ctx context.Context, capability string) error {
	callType := genericCallTypePrefix + capability
	if capability == "" {
		return e.Deny(ctx, callType, cascade.New(cascade.KindInvalidInput, "host: empty capability"))
	}
	if e.policy == nil {
		return e.Deny(ctx, callType, ErrNoPolicyEngine)
	}
	verdict, explanation, err := e.policy.Evaluate(ctx, e.pluginID, capability)
	if err != nil {
		return e.Deny(ctx, callType, cascade.Wrapf(cascade.KindCapabilityDenied, err, "host: evaluating capability %q", capability))
	}
	if verdict != PolicyVerdictAllow {
		reason := explanation
		if reason == "" {
			reason = "policy engine did not allow this capability"
		}
		return e.Deny(ctx, callType, cascade.New(cascade.KindCapabilityDenied, reason))
	}
	return nil
}
