// Package policy (approval_queue_engine.go): Purpose: the S-18 engine
//
//	seam — the injection point and the ask-verdict wiring that sends an
//	action the layers decided to ASK about into the approval queue.
//
// Inputs: an EvalRequest and the EvalOutcome the layers produced.
// Outputs: the same outcome carrying the approval request id, or a DENY
//
//	when the queue refused to accept the action.
//
// Constraints: the engine holds the queue behind the ApprovalQueue
//
//	INTERFACE and never reaches into the implementation, which is what
//	keeps engine.go free of any dependency on the queue's internals and
//	lets S-18.T6 inject its own. An engine with no queue attached is
//	unchanged in behaviour: an ask verdict is still an ask verdict, it
//	simply carries no request id. And a queue refusal DOWNGRADES the
//	outcome to deny rather than leaving it at ask — an action the queue
//	will not carry is an action nobody can ever approve, so presenting it
//	as merely awaiting approval would strand it in a state that reads as
//	permissible.
//
// SPORT: internal/policy Engine/CHANGED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
)

// WithApprovalQueue attaches q to the engine and returns the engine, so
// wiring reads as one expression at the composition root. Passing nil
// detaches the queue.
func (e *Engine) WithApprovalQueue(q ApprovalQueue) *Engine {
	if e == nil {
		return nil
	}
	e.approvals = q
	return e
}

// enqueueAsk sends an ask-verdict action to the queue and folds the answer
// back into the outcome.
//
// A refusal becomes a deny, and the two refusals worth naming are named:
// ErrLocalOnly means §5.14 reserves the action for the local elevation
// helper, and a queue that is full means the user has more pending
// questions than the operator's own cap allows. Every other refusal denies
// with the queue's own reason, because a queue that cannot take the action
// cannot get it approved either.
func (e *Engine) enqueueAsk(ctx context.Context, req EvalRequest, out EvalOutcome) EvalOutcome {
	if e.approvals == nil || safeVerdict(out.Verdict) != VerdictAsk {
		return out
	}
	res, err := e.approvals.Enqueue(ctx, EnqueueRequest{
		Subject:    req.Subject,
		Capability: req.Capability,
		Verb:       req.Verb,
		Level:      out.Level,
		Action:     req.Action,
		Params:     req.Params,
		Summary:    approvalSummary(req),
	})
	if err != nil {
		return denyOutcome(out.Level, out.Layer, askRefusalReason(err))
	}
	out.ApprovalRequestID = res.RequestID
	return out
}

// askRefusalReason renders why the queue would not take the action, in
// words a user can act on.
func askRefusalReason(err error) string {
	switch {
	case errors.Is(err, ErrLocalOnly):
		return "this action is authorized locally in the same turn, never by a queued approval"
	case errors.Is(err, ErrApprovalQueueFull):
		return "the approval queue is full; answer the pending approvals first"
	default:
		return "the approval queue refused this action: " + sanitize(err.Error())
	}
}

// approvalSummary returns the description a surface displays. A caller that
// supplied one gets its own; otherwise the capability name is used, because
// a prompt with no description at all is worse than a terse one.
func approvalSummary(req EvalRequest) string {
	if req.Summary != "" {
		return req.Summary
	}
	return capabilityLabel(req.Capability)
}
