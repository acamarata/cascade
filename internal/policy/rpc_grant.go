// Package policy (rpc_grant.go): Purpose: approval.grant — the one verb
//
//	that turns a recorded decision into a spendable authorization. Split
//	from rpc.go under Art.10.3's 300-line cap; it is the same handler set,
//	kept apart because this is the file where the signature check lives
//	and it should be readable on its own.
//
// Inputs: the request id, an optional SIGNED approval token, and the action
//
//	about to run. With no token, the daemon-held signed record is used, and
//	only under a verified local attestation for approval.grant itself.
//
// Outputs: approvalGrant and its params/result types.
// Constraints: R-21.230 — the signature is VERIFIED BEFORE anything is
//
//	written, so a forged or expired token changes no queue state at all.
//	Knowing a request id is never enough. The sealed audit history is
//	verified, then the redemption is claimed as a create-only audit effect
//	BEFORE the queue's ledger is touched and confirmed after, so a replay,
//	a concurrent second grant and a crash between redeem and confirm all
//	refuse. Every refusal is a KindPermissionDenied (KindInvalidInput for a
//	missing id) and is audited without the token bytes.
//
// SPORT: internal/policy approval-grant-handler/ADDED (P1-E09-W2-S18-T6),
//
//	held-record redemption/CHANGED (P1-SEC-23).
package policy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// grantMethod is the verb whose witness the tokenless path demands.
const grantMethod = "approval.grant"

// ApprovalGrantParams is what approval.grant is called with.
type ApprovalGrantParams struct {
	// RequestID names the approved entry.
	RequestID string `json:"request_id"`
	// SignedToken is the base64 (standard encoding) signed approval
	// record. When empty, the daemon redeems its own held record, which
	// needs a verified local attestation for approval.grant.
	SignedToken string `json:"signed_token"`
	// Action and Params are the action ABOUT TO RUN. The queue re-hashes
	// them and refuses a request that changed after it was approved.
	Action string `json:"action"`
	Params []byte `json:"params"`
}

// ApprovalGrantResult is a completed redemption. It carries no token, no
// nonce and no action hash.
type ApprovalGrantResult struct {
	// RequestID names the redeemed entry.
	RequestID string `json:"request_id"`
	// Capability is the capability it was approved against.
	Capability string `json:"capability"`
	// Level is the rung it was approved at.
	Level string `json:"level"`
	// ConsumedAt is the redemption instant, in RFC3339 nanoseconds.
	ConsumedAt string `json:"consumed_at"`
}

// approvalGrant verifies a signed record and, only then, redeems it once.
//
//  1. the request id must be present;
//  2. the verifier, queue, effect log, audit verifier and audit sink must
//     all be wired — any one absent refuses;
//  3. the record is the caller's token, or (no token) the held record
//     under a verified approval.grant witness;
//  4. it must decode, verify, be unexpired and name THIS request id;
//  5. the sealed audit history must verify;
//  6. the entry must be approved (Decide), live and bound to this action
//     (checked without consuming, so a mistyped action spends nothing) —
//     the grant redeems, it never approves;
//  7. the redemption effect is begun (create-only), the queue consumes,
//     then the effect is confirmed.
func (d RPCDeps) approvalGrant(ctx context.Context, params json.RawMessage) (any, error) {
	var p ApprovalGrantParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval.grant requires a request_id")
	}
	res, err := d.redeemGrant(ctx, p)
	if err != nil {
		return nil, d.auditGrantRefusal(ctx, p.RequestID, err)
	}
	return res, nil
}

// redeemGrant runs steps 2 to 7.
func (d RPCDeps) redeemGrant(ctx context.Context, p ApprovalGrantParams) (ApprovalGrantResult, error) {
	if err := d.grantWired(); err != nil {
		return ApprovalGrantResult{}, err
	}
	raw, err := d.grantRecordBytes(ctx, p)
	if err != nil {
		return ApprovalGrantResult{}, err
	}
	rec, err := d.verifyGrantRecord(raw, p.RequestID)
	if err != nil {
		return ApprovalGrantResult{}, err
	}
	creq := ConsumeRequest{RequestID: p.RequestID, Nonce: rec.Nonce.String(), Action: p.Action, Params: p.Params}
	checker, ok := d.Held.(RedeemChecker)
	if !ok {
		return ApprovalGrantResult{}, cascade.New(cascade.KindPermissionDenied,
			"policy: the held record source cannot check redeemability, so nothing is redeemed")
	}
	if err := checker.CheckRedeemable(creq); err != nil {
		return ApprovalGrantResult{}, grantDenied(err, "the approval does not match this redemption; nothing was spent")
	}
	if err := d.AuditVerify.Verify(ctx); err != nil {
		return ApprovalGrantResult{}, grantDenied(err, "the audit history did not verify")
	}
	h, err := d.Effects.BeginEffect(ctx, audit.EffectRequest{
		Kind: audit.KindApprovalGrant, Actor: "local-operator", Action: grantMethod,
		Key: GrantEffectKey(p.RequestID),
	})
	if err != nil {
		return ApprovalGrantResult{}, grantDenied(err, "this approval was already redeemed or is being redeemed")
	}
	res, err := d.Queue.ConsumeToken(ctx, creq)
	if err != nil {
		_, _ = d.Effects.FailEffect(ctx, h, "approval refused at redemption")
		return ApprovalGrantResult{}, grantDenied(err, "the approval could not be redeemed")
	}
	if _, err := d.Effects.ConfirmEffect(ctx, h, "approval redeemed"); err != nil {
		return ApprovalGrantResult{}, grantDenied(err, "the redemption could not be confirmed")
	}
	return ApprovalGrantResult{
		RequestID: res.RequestID, Capability: res.Capability,
		Level: res.Level.String(), ConsumedAt: canonicalTime(res.ConsumedAt),
	}, nil
}

// grantWired refuses when any collaborator the grant depends on is absent.
func (d RPCDeps) grantWired() error {
	switch {
	case d.Verifier == nil:
		return cascade.New(cascade.KindPermissionDenied,
			"policy: no approval verifier is enrolled, so no approval token can be honoured")
	case d.Queue == nil, d.Held == nil, d.Effects == nil, d.AuditVerify == nil, d.Recorder == nil:
		return cascade.New(cascade.KindPermissionDenied,
			"policy: approval.grant is not fully wired in this process, so nothing is redeemed")
	}
	return nil
}

// grantRecordBytes returns the signed record: the caller's token, or the
// held record when the call carries a verified approval.grant witness.
func (d RPCDeps) grantRecordBytes(ctx context.Context, p ApprovalGrantParams) ([]byte, error) {
	if p.SignedToken != "" {
		raw, err := base64.StdEncoding.DecodeString(p.SignedToken)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindPermissionDenied, ErrInvalidSignature,
				"policy: the approval token is not valid base64")
		}
		return raw, nil
	}
	w, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || w.Method() != grantMethod || d.Attestor == nil {
		return nil, cascade.New(cascade.KindPermissionDenied,
			"policy: a grant without a token needs a local attestation for approval.grant")
	}
	if err := d.Attestor.FreshLocalAttestation(ctx, grantMethod); err != nil {
		return nil, grantDenied(err, "the local attestation was not accepted")
	}
	raw, err := d.Held.HeldSignedRecord(p.RequestID)
	if err != nil {
		return nil, grantDenied(err, "no held approval record is redeemable for this request")
	}
	return raw, nil
}

// verifyGrantRecord checks the signature, expiry and request binding.
func (d RPCDeps) verifyGrantRecord(raw []byte, requestID string) (*ApprovalRecord, error) {
	rec, err := d.Verifier.Verify(raw)
	if err != nil {
		return nil, grantRefusal(err)
	}
	if rec.RequestID.String() != requestID {
		return nil, cascade.Wrapf(cascade.KindPermissionDenied, ErrInvalidSignature,
			"policy: the approval token was issued for a different request")
	}
	return rec, nil
}

// auditGrantRefusal writes one approval.deny row for a refused grant (no
// token bytes, no nonce) and returns the refusal. A refusal that cannot be
// audited is still a refusal.
func (d RPCDeps) auditGrantRefusal(ctx context.Context, requestID string, cause error) error {
	if d.Recorder != nil {
		_, _ = d.Recorder.Append(ctx, audit.Event{
			Kind: audit.KindApprovalDeny, Actor: "local-operator", Action: grantMethod,
			Outcome: "refused: " + sanitize(requestID),
		})
	}
	return cause
}

// grantDenied re-presents any refusal as KindPermissionDenied, keeping the
// cause on the chain.
func grantDenied(err error, why string) error {
	if cascade.HasKind(err, cascade.KindPermissionDenied) {
		return err
	}
	return cascade.Wrapf(cascade.KindPermissionDenied, err, "policy: %s", why)
}

// grantRefusal re-presents a verifier error as a permission denial. The
// verifier already distinguishes expiry from a bad signature for its own
// callers; on this surface both are the same answer, so a caller cannot
// probe the difference.
func grantRefusal(err error) error {
	switch {
	case errors.Is(err, ErrExpired):
		return cascade.Wrapf(cascade.KindPermissionDenied, err,
			"policy: the approval token is no longer valid")
	case errors.Is(err, ErrInvalidSignature):
		return cascade.Wrapf(cascade.KindPermissionDenied, err,
			"policy: the approval token did not verify")
	default:
		return cascade.Wrapf(cascade.KindPermissionDenied, err,
			"policy: the approval token could not be accepted")
	}
}
