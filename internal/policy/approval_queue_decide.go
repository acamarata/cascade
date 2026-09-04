// Package policy (approval_queue_decide.go): Purpose: the half of the
//
//	approval queue where a human's answer is recorded and where an approved
//	token is redeemed exactly once — GetPending, Decide, Cancel,
//	ConsumeToken and the expiry sweep.
//
// Inputs: DecisionRequests carrying the EXACT summary and rung a surface
//
//	displayed, and ConsumeRequests carrying the action about to run.
//
// Outputs: per-element DecisionOutcomes, a ConsumeResult, or a refusal.
// Constraints: three properties, each of which a defect here would break.
//
//	(1) A decision binds to what was shown: an approval whose presented
//	summary differs from the entry's is refused, so a prompt that was
//	rendered from stale state cannot approve the current one. (2) A batch
//	cannot launder risk: an approval whose presented rung is lower than the
//	entry's own rung is refused, and Decide walks its input one element at
//	a time, so a denial is recorded against exactly one entry and its
//	neighbours stay pending. (3) Nothing outlives its window or its scope:
//	expiry, cancellation, a capability that has been de-registered and a
//	revoked standing grant each invalidate a pending or already-granted
//	approval, and redemption re-reads the grant store rather than trusting
//	anything cached at admission.
//
// SPORT: internal/policy DecisionRequest/ADDED, DecisionOutcome/ADDED,
//
//	ConsumeRequest/ADDED, ConsumeResult/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DecisionRequest is one human answer.
type DecisionRequest struct {
	// RequestID names the entry being decided.
	RequestID string
	// Approved is the answer.
	Approved bool
	// PresentedSummary is the EXACT string the surface displayed. An
	// approval whose presented summary differs from the entry's is
	// refused, which is what binds the answer to what was shown.
	PresentedSummary string
	// PresentedLevel is the rung the surface displayed. An approval
	// recorded against a lower rung than the entry carries is refused.
	PresentedLevel RiskLevel
}

// DecisionOutcome is what one element of a Decide call produced. Err is
// per-element on purpose: a refusal on one member of a batch must not
// change any other member's state, and a single error return would force
// the caller to guess how far the batch got.
type DecisionOutcome struct {
	// RequestID names the entry.
	RequestID string
	// State is the entry's state after the call.
	State ApprovalState
	// Err is this element's refusal, or nil.
	Err error
}

// GetPending implements ApprovalQueue. It returns three fields per entry
// and nothing else: no token, no nonce, no action hash, on any code path,
// for any caller, bridge paths included (§5.24).
func (q *StoreApprovals) GetPending(ctx context.Context) ([]PendingEntry, error) {
	if q == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	q.mu.Lock()
	q.pruneLocked(q.cfg.Clock.Now().UTC())
	out := make([]PendingEntry, 0, len(q.order))
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok || e.state != ApprovalPending {
			continue
		}
		out = append(out, PendingEntry{
			RequestID: e.requestID, Summary: e.summary, ExpiresAt: e.expires,
		})
	}
	q.mu.Unlock()
	q.recordExpired(ctx)
	return out, nil
}

// Decide implements ApprovalQueue, one element at a time and in the order
// given, so the result is deterministic and a refusal is confined to the
// element that caused it.
func (q *StoreApprovals) Decide(ctx context.Context, reqs []DecisionRequest) ([]DecisionOutcome, error) {
	if q == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	if len(reqs) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput,
			"policy: approval queue: Decide was given no decisions")
	}
	out := make([]DecisionOutcome, 0, len(reqs))
	decided := make([]approvalEntry, 0, len(reqs))
	q.mu.Lock()
	now := q.cfg.Clock.Now().UTC()
	for _, req := range reqs {
		entry, err := q.decideOneLocked(req, now)
		if err != nil {
			out = append(out, DecisionOutcome{RequestID: req.RequestID, Err: err, State: stateOf(entry)})
			continue
		}
		out = append(out, DecisionOutcome{RequestID: req.RequestID, State: entry.state})
		decided = append(decided, *entry)
	}
	q.mu.Unlock()
	for _, e := range decided {
		q.record(ctx, decisionKind(e.state), e, e.state.String())
	}
	q.recordExpired(ctx)
	return out, nil
}

// stateOf reports a refused element's current state without pretending an
// absent entry has one.
func stateOf(e *approvalEntry) ApprovalState {
	if e == nil {
		return ApprovalPending
	}
	return e.state
}

// decisionKind picks the audit kind for a recorded decision.
func decisionKind(state ApprovalState) audit.Kind {
	if state == ApprovalApproved {
		return audit.KindApprovalGrant
	}
	return audit.KindApprovalDeny
}

// decideOneLocked records one decision, in the refusal order the contract
// fixes: unknown id, already decided, expired, then — for an approval
// only — the two bindings that stop a batch from laundering risk.
func (q *StoreApprovals) decideOneLocked(req DecisionRequest, now time.Time) (*approvalEntry, error) {
	entry, err := q.liveEntryLocked(req.RequestID, now)
	if err != nil {
		return entry, err
	}
	if !req.Approved {
		entry.state = ApprovalDenied
		return entry, nil
	}
	if req.PresentedSummary != entry.summary {
		return entry, refuse(ErrApprovalMismatch,
			"request %s was approved against a different description than the one queued",
			sanitize(req.RequestID))
	}
	if !req.PresentedLevel.Valid() || safeLevel(req.PresentedLevel) < entry.level {
		return entry, refuse(ErrApprovalRungMismatch,
			"request %s is %s but was approved at %s",
			sanitize(req.RequestID), entry.level, safeLevel(req.PresentedLevel))
	}
	entry.state = ApprovalApproved
	return entry, nil
}

// liveEntryLocked returns the still-pending entry for id, or the refusal
// that says why there is not one.
func (q *StoreApprovals) liveEntryLocked(id string, now time.Time) (*approvalEntry, error) {
	entry, ok := q.entries[id]
	if !ok {
		return nil, refuse(ErrUnknownRequest, "no approval request named %q", sanitize(id))
	}
	// Expiry is checked BEFORE the state, so an entry that lapsed after it
	// was approved reports "expired" rather than "already decided". The
	// distinction matters to the user: one says the answer came too late,
	// the other says somebody already answered.
	if err := q.expireIfLapsedLocked(entry, now); err != nil {
		return entry, err
	}
	if entry.state != ApprovalPending {
		return entry, terminalRefusal(entry.state, id)
	}
	return entry, nil
}

// expireIfLapsedLocked retires an entry whose exp has passed and returns
// the refusal, or nil when it is still live. It is the one place a lapse is
// turned into a state change, so no path can read a lapsed entry as usable.
func (q *StoreApprovals) expireIfLapsedLocked(entry *approvalEntry, now time.Time) error {
	if entry.state != ApprovalPending && entry.state != ApprovalApproved {
		return nil
	}
	if now.Before(entry.expires) {
		return nil
	}
	entry.state = ApprovalExpired
	q.expiredAudit = append(q.expiredAudit, *entry)
	return refuse(ErrTokenExpired, "request %s expired at %s",
		sanitize(entry.requestID), entry.expires.Format(time.RFC3339))
}

// terminalRefusal maps a non-pending state to its refusal. Each state gets
// its own so a caller can tell a replay from a cancellation from an
// expiry; all of them deny.
func terminalRefusal(state ApprovalState, id string) error {
	switch state {
	case ApprovalConsumed:
		return refuse(ErrTokenReplayed, "request %s has already been redeemed", sanitize(id))
	case ApprovalExpired:
		return refuse(ErrTokenExpired, "request %s expired", sanitize(id))
	case ApprovalCanceled:
		return refuse(ErrApprovalCanceled, "request %s was withdrawn", sanitize(id))
	case ApprovalApproved, ApprovalDenied:
		return refuse(ErrApprovalDecided, "request %s was already decided (%s)", sanitize(id), state)
	case ApprovalPending:
		return nil
	default:
		return refuse(ErrApprovalNotApproved, "request %s is in no valid state", sanitize(id))
	}
}

// Cancel implements ApprovalQueue. A withdrawn entry is terminal: its
// token can never be redeemed, whatever its exp says.
func (q *StoreApprovals) Cancel(ctx context.Context, requestID string) error {
	if q == nil {
		return cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	q.mu.Lock()
	entry, ok := q.entries[requestID]
	if !ok {
		q.mu.Unlock()
		return refuse(ErrUnknownRequest, "no approval request named %q", sanitize(requestID))
	}
	if entry.state != ApprovalPending && entry.state != ApprovalApproved {
		state := entry.state
		q.mu.Unlock()
		return terminalRefusal(state, requestID)
	}
	entry.state = ApprovalCanceled
	snapshot := *entry
	q.mu.Unlock()
	q.record(ctx, audit.KindApprovalDeny, snapshot, ApprovalCanceled.String())
	return nil
}
