// Purpose: the daemon-held signed approval record: the queue keeps the
//
//	bytes a signing minter produced beside the entry, hands them to the
//	attested local grant path only, and recovery closes grant effects a
//	crash left open.
//
// Inputs: a request id; for recovery, the audit effect log.
// Outputs: HeldRecordSource, RedeemChecker, StoreApprovals.HeldSignedRecord,
//
//	StoreApprovals.CheckRedeemable, RecoverGrantEffects, GrantEffectKey.
//
// Constraints: the held bytes never reach GetPending, list/show JSON, a
//
//	bridge payload or the journal; their one production reader is the
//	witnessed approval.grant, which only verifies them. Any retained signed
//	entry that is not consumed or being consumed has a held record, whatever
//	its exp, so the grant can record the decision on a pending entry and
//	refuse the others with their own state's refusal. A request that is
//	unknown, consumed, consuming or was minted unsigned has none
//	(KindNotFound).
//
// SPORT: internal/policy HeldRecordSource/ADDED, HeldSignedRecord/ADDED (P1-SEC-23),
//
//	HeldSignedRecord states/CHANGED (P1-SEC-41).

package policy

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// HeldRecordSource hands over the signed record the daemon holds for a
// request. Only approval.grant's attested tokenless path reads it.
type HeldRecordSource interface {
	HeldSignedRecord(requestID string) ([]byte, error)
}

var _ HeldRecordSource = (*StoreApprovals)(nil)

// RedeemChecker answers, without consuming, whether ConsumeToken would
// accept req now (state, expiry, nonce and the action binding). The grant
// asks it before claiming the redemption effect, so a mistyped action is
// refused without spending the approval. A held source without it makes
// every grant refuse.
type RedeemChecker interface {
	CheckRedeemable(req ConsumeRequest) error
}

var _ RedeemChecker = (*StoreApprovals)(nil)

// CheckRedeemable implements RedeemChecker with ConsumeToken's own steps 1
// to 4; ConsumeToken re-runs them under its claim, so this only spares a
// doomed claim.
func (q *StoreApprovals) CheckRedeemable(req ConsumeRequest) error {
	if q == nil {
		return cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	_, err := q.redeemableLocked(req)
	return err
}

// grantEffectPrefix keys every redemption claim.
const grantEffectPrefix = "approval.grant:"

// GrantEffectKey is the audit effect key one request's redemption claims.
func GrantEffectKey(requestID string) string { return grantEffectPrefix + requestID }

// HeldSignedRecord implements HeldRecordSource. It returns a copy for any
// retained, signed entry that is not consumed or consuming (pending,
// approved, denied, canceled or expired, whatever its exp): the grant
// verifies it and then reads the entry's state, which is what refuses a
// non-pending entry. A consumed or consuming entry has no held record, so a
// grant racing the owner's redemption refuses before any effect is begun.
func (q *StoreApprovals) HeldSignedRecord(requestID string) ([]byte, error) {
	if q == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry, ok := q.entries[requestID]
	if !ok || len(entry.signed) == 0 || entry.state == ApprovalConsumed || entry.state == ApprovalConsuming {
		return nil, cascade.Newf(cascade.KindNotFound,
			"policy: no held approval record for request %q", sanitize(requestID))
	}
	return append([]byte(nil), entry.signed...), nil
}

// grantRecovery is the slice of the effect log recovery needs.
type grantRecovery interface {
	PendingEffects(ctx context.Context) ([]audit.EffectHandle, error)
	MarkUnknownOutcome(ctx context.Context, h audit.EffectHandle) (audit.Record, error)
}

// RecoverGrantEffects marks every approval.grant intent a crash left open as
// unknown-outcome. The key stays claimed, so the request never redeems
// again. It returns how many it closed.
func RecoverGrantEffects(ctx context.Context, effects grantRecovery) (int, error) {
	if effects == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "policy: grant recovery needs the effect log")
	}
	pending, err := effects.PendingEffects(ctx)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, h := range pending {
		if h.Kind != audit.KindApprovalGrant || !strings.HasPrefix(h.Key, grantEffectPrefix) {
			continue
		}
		if _, err := effects.MarkUnknownOutcome(ctx, h); err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}
