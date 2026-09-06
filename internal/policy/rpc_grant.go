// Package policy (rpc_grant.go): Purpose: approval.grant — the one verb
//
//	that turns a recorded decision into a spendable authorization. Split
//	from rpc.go under Art.10.3's 300-line cap; it is the same handler set,
//	kept apart because this is the file where the signature check lives
//	and it should be readable on its own.
//
// Inputs: the request id, the SIGNED approval token, and the action about
//
//	to run.
//
// Outputs: approvalGrant and its params/result types.
// Constraints: R-21.230 — the signature is VERIFIED BEFORE the ledger
//
//	append, so a forged or expired token changes no queue state at all.
//	Knowing a request id is never enough. Every refusal here is a
//	KindPermissionDenied, so a caller cannot tell a forged signature from
//	an unknown id by the kind it gets back.
//
// SPORT: internal/policy approval-grant-handler/ADDED (P1-E09-W2-S18-T6).
package policy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalGrantParams is what approval.grant is called with.
type ApprovalGrantParams struct {
	// RequestID names the approved entry.
	RequestID string `json:"request_id"`
	// SignedToken is the base64 (standard encoding) signed approval
	// record. It is REQUIRED: this is what makes approval authority
	// something a caller holds rather than something a caller knows.
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

// approvalGrant verifies the signed token and, only then, redeems it.
//
// The order below is the ruling, and every step refuses:
//
//  1. the request id and the token must both be present;
//  2. a verifier must be enrolled — with none, there is nothing that can
//     tell a real token from an invented one, so the verb refuses rather
//     than falling back to the id alone;
//  3. the token must decode, verify and be unexpired;
//  4. the token must name THIS request id;
//  5. only now is the queue's single-use ledger touched.
func (d RPCDeps) approvalGrant(ctx context.Context, params json.RawMessage) (any, error) {
	var p ApprovalGrantParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" || p.SignedToken == "" {
		return nil, cascade.New(cascade.KindInvalidInput,
			"policy: approval.grant requires a request_id and a signed_token")
	}
	rec, err := d.verifyGrantToken(p)
	if err != nil {
		return nil, err
	}
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	res, err := queue.ConsumeToken(ctx, ConsumeRequest{
		RequestID: p.RequestID,
		Nonce:     rec.Nonce.String(),
		Action:    p.Action,
		Params:    p.Params,
	})
	if err != nil {
		return nil, err
	}
	return ApprovalGrantResult{
		RequestID:  res.RequestID,
		Capability: res.Capability,
		Level:      res.Level.String(),
		ConsumedAt: canonicalTime(res.ConsumedAt),
	}, nil
}

// verifyGrantToken performs steps 2 to 4 above. It returns the verified
// record, or a KindPermissionDenied refusal that says as little as the
// caller needs to know.
func (d RPCDeps) verifyGrantToken(p ApprovalGrantParams) (*ApprovalRecord, error) {
	if d.Verifier == nil {
		return nil, cascade.New(cascade.KindPermissionDenied,
			"policy: no approval verifier is enrolled, so no approval token can be honoured")
	}
	raw, err := base64.StdEncoding.DecodeString(p.SignedToken)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindPermissionDenied, ErrInvalidSignature,
			"policy: the approval token is not valid base64")
	}
	rec, err := d.Verifier.Verify(raw)
	if err != nil {
		return nil, grantRefusal(err)
	}
	if rec.RequestID.String() != p.RequestID {
		return nil, cascade.Wrapf(cascade.KindPermissionDenied, ErrInvalidSignature,
			"policy: the approval token was issued for a different request")
	}
	return rec, nil
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
