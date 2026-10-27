// Purpose: approval.grant without a token, the held-record decision: an
//
//	elevated, witnessed, audited human approval of a pending entry. It
//	records the decision and never redeems; the component that queued the
//	entry holds its nonce and redeems it with its own ConsumeToken, once.
//
// Inputs: the request id, under a verified approval.grant witness.
// Outputs: decideHeld, which answers a DecisionResult{request_id, state}.
// Constraints: in this order, each refusing: the witness and the held
//
//	record read and verify (rpc_grant.go); no entry source (KindUnavailable)
//	before any effect; the sealed audit history; the create-only effect
//	"approval.grant:<id>" (a second grant refuses there); the entry's state,
//	which must be pending; Decide(Approved) with the summary and rung the
//	ENTRY carries, never the caller's. Any refusal after the effect is begun
//	fails the effect. ConsumeToken is never called here.
//
// SPORT: internal/policy held-record decision/ADDED (P1-SEC-41).

package policy

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// heldApprovedOutcome is the effect outcome of a recorded approval.
const heldApprovedOutcome = "approved"

// decideHeld records the human approval of one pending entry.
func (d RPCDeps) decideHeld(ctx context.Context, p ApprovalGrantParams) (DecisionResult, error) {
	if err := d.grantWired(); err != nil {
		return DecisionResult{}, err
	}
	raw, err := d.grantRecordBytes(ctx, p)
	if err != nil {
		return DecisionResult{}, err
	}
	if _, err := d.verifyGrantRecord(raw, p.RequestID); err != nil {
		return DecisionResult{}, err
	}
	if d.Entries == nil {
		return DecisionResult{}, cascade.New(cascade.KindUnavailable,
			"policy: no approval entry source is wired in this process, so nothing is approved")
	}
	if err := d.AuditVerify.Verify(ctx); err != nil {
		return DecisionResult{}, grantDenied(err, "the audit history did not verify")
	}
	h, err := d.Effects.BeginEffect(ctx, audit.EffectRequest{
		Kind: audit.KindApprovalGrant, Actor: "local-operator", Action: grantMethod,
		Key: GrantEffectKey(p.RequestID),
	})
	if err != nil {
		return DecisionResult{}, grantDenied(err, "this request was already granted or is being granted")
	}
	if err := d.approvePending(ctx, p.RequestID); err != nil {
		_, _ = d.Effects.FailEffect(ctx, h, "approval refused")
		return DecisionResult{}, err
	}
	if _, err := d.Effects.ConfirmEffect(ctx, h, heldApprovedOutcome); err != nil {
		return DecisionResult{}, grantDenied(err, "the approval was recorded, but its confirmation could not be "+
			"written; this request cannot be granted again")
	}
	return DecisionResult{RequestID: p.RequestID, State: ApprovalApproved.String()}, nil
}

// approvePending reads the entry and, only when it is pending, records the
// approval against the summary and rung the entry itself carries.
func (d RPCDeps) approvePending(ctx context.Context, requestID string) error {
	view, err := d.Entries.EntryView(ctx, requestID)
	if err != nil {
		return err
	}
	switch {
	case !view.State.Valid():
		return cascade.Newf(cascade.KindIntegrity,
			"policy: approval request %q is in no valid state; nothing is approved", sanitize(requestID))
	case view.State != ApprovalPending:
		return grantDenied(terminalRefusal(view.State, requestID), "only a pending request can be approved")
	}
	outcomes, err := d.Queue.Decide(ctx, []DecisionRequest{{
		RequestID: requestID, Approved: true, PresentedSummary: view.Summary, PresentedLevel: view.Level,
	}})
	if err != nil {
		return grantDenied(err, "the approval could not be recorded")
	}
	if len(outcomes) != 1 {
		return cascade.New(cascade.KindPermissionDenied,
			"policy: the approval queue answered a one-element decision with a different number of outcomes")
	}
	if outcomes[0].Err != nil {
		return grantDenied(outcomes[0].Err, "the approval was refused")
	}
	return nil
}
