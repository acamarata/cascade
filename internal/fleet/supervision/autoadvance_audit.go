// Purpose: the audit record for every auto-advance verdict — including the
//
//	approvals, which are the ones nobody is prompted about.
//
// WHY EVERY VERDICT AND NOT JUST THE REFUSALS. An auto-approval is the
//
//	only decision in this system that runs with no human turn. If it is not
//	recorded, there is no way to answer "what did the machine do while I
//	was away", which is the whole question tier-1 supervision exists to
//	make answerable. The refusals are recorded too, because an operator
//	tuning their ceiling needs to see what it stopped.
//
// Inputs: an audit.Writer, the action, and the verdict.
// Outputs: one audit record per verdict.
// Constraints: the record carries NO command text, NO parameters and NO
//
//	vault material — only the action's reference, its origin, the risk
//	level, the trust tag and the verdict. It reuses the frozen
//	`policy.route` kind rather than adding a fifteenth: the fourteen kinds
//	are ratified, and this decision happens inside the router.
//
// SPORT: internal/fleet/supervision:autoadvance-audit (ADD) — P1-E18-W4-S39-T2.

package supervision

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
)

// autoAdvanceActor names this stage in the audit log.
const autoAdvanceActor = "supervision.autoadvance"

// autoAdvanceExplain is the rationale payload. Each field answers "why
// this verdict" without reproducing anything the action carried.
type autoAdvanceExplain struct {
	Origin  string `json:"origin,omitempty"`
	Verb    string `json:"verb,omitempty"`
	Trust   string `json:"trust,omitempty"`
	Verdict string `json:"verdict"`
	// Elevated records the §5.14 answer that was used, so a later reader
	// can tell an elevated_refused from a ceiling_refused that happened to
	// involve a verb.
	Elevated bool `json:"elevated"`
}

// RecordAutoAdvance writes one verdict to the audit log.
//
// A nil writer is a no-op rather than an error: this runs inside the
// authorization middleware, and a missing audit sink must not turn a
// decided action into a failed one. The router's own record of the routing
// decision is written separately and unconditionally, so the decision
// itself is never unlogged — this adds the auto-advance detail on top.
func RecordAutoAdvance(
	ctx context.Context, writer audit.Writer,
	action ActionDescriptor, decision PolicyDecision, trust TrustTag, verdict Verdict,
) error {
	if writer == nil {
		return nil
	}
	explain, err := json.Marshal(autoAdvanceExplain{
		Origin:   action.Origin,
		Verb:     action.Verb,
		Trust:    string(trust),
		Verdict:  verdict.String(),
		Elevated: action.Elevated,
	})
	if err != nil {
		return err
	}
	_, err = writer.Append(ctx, audit.Event{
		Kind:      audit.KindPolicyRoute,
		Actor:     autoAdvanceActor,
		Action:    action.Ref,
		RiskLevel: decision.Level.String(),
		Verdict:   verdict.String(),
		Explain:   explain,
	})
	return err
}

// AutoAdvanceRecorder writes each verdict and queues the refusals. It is
// the one object a composition root hands the router, so the two
// side-effects of a verdict travel together and neither can be wired
// without the other.
type AutoAdvanceRecorder struct {
	writer audit.Writer
	pusher AttentionPusher
	scope  ScopeRef
}

// NewAutoAdvanceRecorder builds the recorder. Both sinks may be nil, which
// is how a partially-wired daemon degrades: the verdict still decides, it
// just leaves less of a trail. Neither absence can change a decision.
func NewAutoAdvanceRecorder(writer audit.Writer, pusher AttentionPusher, scopeRef ScopeRef) *AutoAdvanceRecorder {
	return &AutoAdvanceRecorder{writer: writer, pusher: pusher, scope: scopeRef}
}

// Record audits the verdict and queues it when it is a refusal a human
// must act on.
//
// The audit write happens FIRST and its error is returned even when the
// queue push then succeeds: the record is the thing that must exist for
// the decision to be answerable later, and a queued item with no audit row
// is a prompt whose origin cannot be reconstructed.
func (r *AutoAdvanceRecorder) Record(
	ctx context.Context, action ActionDescriptor,
	decision PolicyDecision, trust TrustTag, verdict Verdict,
) error {
	auditErr := RecordAutoAdvance(ctx, r.writer, action, decision, trust, verdict)
	if _, err := QueueAutoAdvanceRefusal(ctx, r.pusher, action, r.scope, verdict); err != nil && auditErr == nil {
		return err
	}
	return auditErr
}
