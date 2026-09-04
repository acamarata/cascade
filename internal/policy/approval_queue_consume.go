// Package policy (approval_queue_consume.go): Purpose: redemption — the
//
//	one moment an approval becomes an action, and the point at which every
//	binding the queue made is checked again against the world as it is now
//	rather than as it was when the question was asked.
//
// Inputs: a ConsumeRequest carrying the request id, the token's nonce, and
//
//	the action ABOUT TO RUN.
//
// Outputs: a ConsumeResult, or a refusal. There is no third outcome.
// Constraints: the seven ordered refusals in ConsumeToken's doc comment
//
//	are the contract, and the two that matter most are the ones a naive
//	implementation omits. The action and parameters are RE-HASHED from what
//	is about to run and compared with the digests the approval was issued
//	against, so an approval given for one request cannot be spent on a
//	mutated one. And the standing grant the entry was admitted under is
//	RE-READ from the store with no cache, so revoking that grant
//	invalidates an approval that has already been granted — which is the
//	whole point of the grant model having no cache in the first place.
//	The durable ledger claim is last, and it is a conditional create, so
//	two concurrent redemptions of one token cannot both succeed.
//
// SPORT: internal/policy ConsumeRequest/ADDED, ConsumeResult/ADDED
//
//	(P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ConsumeRequest redeems one approved token.
type ConsumeRequest struct {
	// RequestID names the approved entry.
	RequestID string
	// Nonce is the token's single-use value.
	Nonce string
	// Action and Params are the action ABOUT TO RUN. They are re-hashed
	// and compared against the digests the approval was issued for, so a
	// request that changed after it was approved is refused.
	Action string
	Params []byte
}

// ConsumeResult is a successful redemption.
type ConsumeResult struct {
	// RequestID names the redeemed entry.
	RequestID string
	// Subject is the principal the action runs as.
	Subject Subject
	// Capability is the capability it was approved against.
	Capability string
	// Level is the rung it was approved at.
	Level RiskLevel
	// ConsumedAt is the redemption instant.
	ConsumedAt time.Time
}

// ConsumeToken implements ApprovalQueue: the single redemption of one
// approved token, against the action it was approved for.
//
// The order below is the contract, and every step denies:
//
//  1. the entry must exist and be APPROVED — pending, denied, cancelled,
//     expired and already-consumed each refuse with their own reason;
//  2. the token must not have expired;
//  3. the nonce must match, compared in constant time;
//  4. the action and parameters ABOUT TO RUN must re-hash to the digests
//     the approval was issued against — this is what makes an approval
//     unusable for a mutated request;
//  5. the capability must still be registered;
//  6. a standing grant the entry was admitted under must still be in
//     force, re-read from the store with no cache;
//  7. the nonce must be claimable in the durable ledger, which is what
//     makes redemption single-use across a restart.
func (q *StoreApprovals) ConsumeToken(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	if q == nil {
		return ConsumeResult{}, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	snapshot, err := q.redeemableLocked(req)
	if err != nil {
		return ConsumeResult{}, err
	}
	if err := q.revalidate(ctx, snapshot); err != nil {
		return ConsumeResult{}, err
	}
	now := q.cfg.Clock.Now().UTC()
	if err := q.ledger.Consume(ctx, LedgerRecord{
		Nonce:      snapshot.nonce,
		RequestID:  snapshot.requestID,
		ActionHash: snapshot.actionHash,
		Subject:    snapshot.subject.String(),
	}); err != nil {
		return ConsumeResult{}, err
	}
	q.mu.Lock()
	if entry, ok := q.entries[snapshot.requestID]; ok {
		entry.state = ApprovalConsumed
	}
	q.mu.Unlock()
	q.record(ctx, audit.KindApprovalGrant, snapshot, ApprovalConsumed.String())
	return ConsumeResult{
		RequestID: snapshot.requestID, Subject: snapshot.subject,
		Capability: snapshot.capability, Level: snapshot.level, ConsumedAt: now,
	}, nil
}

// redeemableLocked runs steps 1 to 4: the state, expiry, nonce and
// action-binding checks, all under the queue's lock.
func (q *StoreApprovals) redeemableLocked(req ConsumeRequest) (approvalEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.cfg.Clock.Now().UTC()
	entry, ok := q.entries[req.RequestID]
	if !ok {
		return approvalEntry{}, refuse(ErrUnknownRequest,
			"no approval request named %q", sanitize(req.RequestID))
	}
	// Expiry first, so a lapsed approval reports the lapse rather than
	// whatever state it happened to be sitting in.
	if err := q.expireIfLapsedLocked(entry, now); err != nil {
		return *entry, err
	}
	if entry.state != ApprovalApproved {
		if entry.state == ApprovalPending || entry.state == ApprovalDenied {
			return *entry, refuse(ErrApprovalNotApproved,
				"request %s is %s, and only an approved request may be redeemed",
				sanitize(req.RequestID), entry.state)
		}
		return *entry, terminalRefusal(entry.state, req.RequestID)
	}
	if subtle.ConstantTimeCompare([]byte(entry.nonce), []byte(req.Nonce)) != 1 {
		return *entry, refuse(ErrApprovalMismatch,
			"request %s was redeemed with a nonce it was not issued", sanitize(req.RequestID))
	}
	if hashApproval([]byte(req.Action)) != entry.actionHash ||
		hashApproval(req.Params) != entry.paramsHash {
		return *entry, refuse(ErrApprovalMismatch,
			"request %s was approved for a different action than the one being run",
			sanitize(req.RequestID))
	}
	return *entry, nil
}

// revalidate runs steps 5 and 6: the capability must still be registered,
// and a standing grant the entry was admitted under must still be in
// force. Both are re-read, never cached — which is the whole reason
// revoking a grant invalidates an approval that was already given.
func (q *StoreApprovals) revalidate(ctx context.Context, entry approvalEntry) error {
	if _, err := q.cfg.Registry.Lookup(ctx, entry.capability); err != nil {
		return err
	}
	if !entry.grantBacked {
		return nil
	}
	d, err := q.cfg.Grants.Check(ctx, CheckRequest{
		Subject: entry.subject, Capability: entry.capability,
	})
	if err != nil {
		return err
	}
	if !d.Granted {
		return newGrantDenied("the grant request %s was approved under has been revoked",
			sanitize(entry.requestID))
	}
	return nil
}
