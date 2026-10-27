// Purpose: one queued entry's state, read without its secrets, so
//
//	approval.show can report approved versus consumed and the held grant
//	can refuse an entry that is no longer pending.
//
// Inputs: a request id.
// Outputs: ApprovalEntryView, ApprovalEntrySource, StoreApprovals.EntryView.
// Constraints: the view carries no nonce, token, signed record, action hash
//
//	or params hash. A lapsed entry reads expired (the same expiry step every
//	other read takes); an unknown or pruned id is ErrUnknownRequest; a state
//	outside the valid set is KindIntegrity and never reads as pending. This is
//	a StoreApprovals method, not an ApprovalQueue one, because the dry-run
//	preview queue implements that interface too.
//
// SPORT: internal/policy ApprovalEntryView/ADDED, ApprovalEntrySource/ADDED (P1-SEC-41).

package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalEntryView is the secret-free state of one retained entry.
type ApprovalEntryView struct {
	// RequestID names the entry.
	RequestID string
	// Summary is the exact string a surface displays (the string Decide
	// binds an approval to).
	Summary string
	// Capability is the capability the entry was queued against.
	Capability string
	// Level is the entry's rung.
	Level RiskLevel
	// State is where the entry stands now.
	State ApprovalState
	// ExpiresAt is the entry's exp.
	ExpiresAt time.Time
}

// ApprovalEntrySource reads one retained entry's state.
type ApprovalEntrySource interface {
	EntryView(ctx context.Context, requestID string) (ApprovalEntryView, error)
}

var _ ApprovalEntrySource = (*StoreApprovals)(nil)

// EntryView implements ApprovalEntrySource. It prunes and applies the
// expiry step under the queue's lock, so a lapsed entry reads expired, then
// writes any expiry rows after unlocking, as GetPending does.
func (q *StoreApprovals) EntryView(ctx context.Context, requestID string) (ApprovalEntryView, error) {
	if q == nil {
		return ApprovalEntryView{}, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	q.mu.Lock()
	now := q.cfg.Clock.Now().UTC()
	q.pruneLocked(now)
	entry, ok := q.entries[requestID]
	var view ApprovalEntryView
	if ok {
		_ = q.expireIfLapsedLocked(entry, now) // the lapse is reported as the expired state
		view = ApprovalEntryView{RequestID: entry.requestID, Summary: entry.summary, Capability: entry.capability,
			Level: entry.level, State: entry.state, ExpiresAt: entry.expires}
	}
	q.mu.Unlock()
	q.recordExpired(ctx)
	switch {
	case !ok:
		return ApprovalEntryView{}, refuse(ErrUnknownRequest, "no approval request named %q", sanitize(requestID))
	case !view.State.Valid():
		return ApprovalEntryView{}, cascade.Newf(cascade.KindIntegrity,
			"policy: approval request %q is in no valid state", sanitize(requestID))
	}
	return view, nil
}
