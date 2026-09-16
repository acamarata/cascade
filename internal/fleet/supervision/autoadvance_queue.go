// Purpose: routing a refused auto-advance to the attention queue, so the
//
//	action a human must decide actually reaches a human.
//
// WHY THIS EXISTS SEPARATELY FROM THE VERDICT. A refusal that is only
//
//	audited is a refusal nobody sees until they go looking. The queue is
//	the surface an operator watches; a ceiling that silently declined would
//	stall work with no visible reason, which is worse than not having the
//	ceiling.
//
// Inputs: an attention pusher, the action, and the verdict.
// Outputs: at most one AttentionItem.
// Constraints: only the three REFUSAL verdicts queue. An approval has
//
//	nothing for a human to do, and fail_closed_denied is a denial the
//	policy layer already surfaces through its own path — queueing it would
//	invite an operator to "approve" something the engine refused outright.
//
// SPORT: internal/fleet/supervision:autoadvance-queue (ADD) — P1-E18-W4-S39-T2.

package supervision

import "context"

// AttentionPusher is the queue seam. *Store satisfies it.
type AttentionPusher interface {
	Push(ctx context.Context, item AttentionItem) (AttentionItem, error)
}

// autoAdvanceKinds maps each queueable verdict to the attention kind an
// operator should see.
//
// elevated_refused gets its own kind because the remedy is different in
// kind, not just in degree: a ceiling refusal is answered by deciding the
// action, an elevation refusal by proving local presence.
var autoAdvanceKinds = map[Verdict]Kind{
	VerdictCeilingRefused:   KindPolicyAsk,
	VerdictUntrustedRefused: KindPolicyAsk,
	VerdictElevatedRefused:  KindElevationRefused,
}

// autoAdvancePriority orders queued refusals. An elevation refusal sorts
// ahead of an ordinary ask: it is blocking on a human act that only a
// person at the machine can perform, so it is the one most likely to be
// holding something up.
const (
	autoAdvancePriorityElevation = 1
	autoAdvancePriorityAsk       = 2
)

// QueueAutoAdvanceRefusal files a refused verdict for human attention.
//
// queued reports whether an item was actually pushed, so a caller can tell
// "nothing to queue" from "queued successfully" without inspecting the
// returned item. A nil pusher is a no-op rather than an error, for the same
// reason the audit writer is: this runs inside the authorization
// middleware, and an unwired queue must not turn a decided action into a
// failed one.
func QueueAutoAdvanceRefusal(
	ctx context.Context, pusher AttentionPusher,
	action ActionDescriptor, scopeRef ScopeRef, verdict Verdict,
) (queued bool, err error) {
	kind, queueable := autoAdvanceKinds[verdict]
	if !queueable || pusher == nil {
		return false, nil
	}
	priority := autoAdvancePriorityAsk
	if kind == KindElevationRefused {
		priority = autoAdvancePriorityElevation
	}
	if _, err := pusher.Push(ctx, AttentionItem{
		Kind:      kind,
		SourceRef: action.Ref,
		ScopeRef:  scopeRef,
		Priority:  priority,
	}); err != nil {
		return false, err
	}
	return true, nil
}
