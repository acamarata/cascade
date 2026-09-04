package memory

// Purpose: the Q-6 promotion ladder: the thresholds a candidate must cross
//   to become a durable record, the typed events every state transition
//   emits, and the sink those events are reported to.
// Inputs: observations, forwarded to the CandidateLedger the ladder wraps.
// Outputs: the candidate's state after the observation, whether that
//   observation promoted it, and a typed pkg/cascade error on refusal.
// Constraints: the decision is a pure function of the candidate's counted
//   state, so the same evidence always produces the same answer on any
//   machine and in any order; nothing here reads a clock or a map.
// SPORT: G/memory-promotion-ladder (ADD, placeholder per T-1
//   sport_updates).

import (
	"context"
	"time"
)

// The Q-6 thresholds. A candidate is promoted once it has been referenced
// at least PromotionMinRefCount times across at least PromotionMinSessions
// distinct sessions.
//
// Both conditions are evaluated, and both are stated here rather than
// folded into one number, because they answer different questions: the
// reference count asks whether the observation recurred, and the session
// count asks whether it recurred somewhere other than the conversation
// that first produced it. A belief attested only inside a single session
// is exactly the one most likely to be an artifact of that session.
const (
	// PromotionMinRefCount is the minimum counted references.
	PromotionMinRefCount = 3
	// PromotionMinSessions is the minimum distinct observing sessions.
	PromotionMinSessions = 2
)

// Event names, as the audit bus reads them.
const (
	// CandidatePromotedEvent names the promotion transition.
	CandidatePromotedEvent = "memory.candidate.promoted"
	// CandidateRevertedEvent names the revert transition.
	CandidateRevertedEvent = "memory.candidate.reverted"
)

// PromotionEvent reports that a candidate became a durable record. It
// carries the evidence the decision was taken on, not only the outcome, so
// a later reader can tell why the system came to believe something rather
// than only that it does.
type PromotionEvent struct {
	// Name and Kind identify the record that was written.
	Name string
	Kind MemoryKind
	// RefCount is how many references were counted at promotion.
	RefCount int
	// SessionIDs are the distinct sessions that observed it, in lexical
	// order.
	SessionIDs []string
	// PromotedAt is the instant of the transition, from the injected
	// clock.
	PromotedAt time.Time
}

// EventName returns the audit-bus name of this event.
func (PromotionEvent) EventName() string { return CandidatePromotedEvent }

// RevertEvent reports that a promotion was taken back.
type RevertEvent struct {
	// Name and Kind identify the candidate.
	Name string
	Kind MemoryKind
	// Reason is the caller's stated reason, which may be empty.
	Reason string
	// RevertedAt is the instant of the transition, from the injected
	// clock.
	RevertedAt time.Time
}

// EventName returns the audit-bus name of this event.
func (RevertEvent) EventName() string { return CandidateRevertedEvent }

// CandidateEventSink receives one event per candidate state transition.
//
// It is declared here, at the point of use, rather than imported from the
// event bus: the ledger depends on the shape of a sink, not on any
// particular bus, and the audit bus is wired in by the surface that owns
// both. An error from a sink is returned to the ledger's caller rather
// than swallowed; the state change has already been persisted by then, and
// saying so honestly is better than reporting a transition nobody
// recorded.
type CandidateEventSink interface {
	// CandidatePromoted reports a promotion.
	CandidatePromoted(ctx context.Context, ev PromotionEvent) error
	// CandidateReverted reports a revert.
	CandidateReverted(ctx context.Context, ev RevertEvent) error
}

// discardCandidateEvents is the sink a ledger built with no sink uses. It
// is a real, complete implementation, not a placeholder: a caller with no
// event bus is a supported configuration, and discarding an event never
// changes what the ledger stores or decides.
type discardCandidateEvents struct{}

// CandidatePromoted discards the event.
func (discardCandidateEvents) CandidatePromoted(context.Context, PromotionEvent) error { return nil }

// CandidateReverted discards the event.
func (discardCandidateEvents) CandidateReverted(context.Context, RevertEvent) error { return nil }

// promotionEventOf builds the promotion event from the state that was
// promoted, so the event and the stored record cannot disagree about the
// evidence.
func promotionEventOf(c CandidateEntry, at time.Time) PromotionEvent {
	ids := make([]string, len(c.SessionIDs))
	copy(ids, c.SessionIDs)
	return PromotionEvent{
		Name: c.Name, Kind: c.Kind, RefCount: c.RefCount,
		SessionIDs: ids, PromotedAt: at,
	}
}

// ReadyForPromotion reports whether a candidate has crossed both Q-6
// thresholds.
//
// It is a pure function of the counted state and consults nothing else:
// the same candidate always gets the same answer, on any machine, in any
// order, with no clock and no map iteration involved. A candidate that is
// not pending is never ready, so neither a promoted candidate nor a
// reverted one can be promoted by this decision without first being
// observed again.
func ReadyForPromotion(c CandidateEntry) bool {
	if c.Status != CandidatePending {
		return false
	}
	return c.RefCount >= PromotionMinRefCount && len(c.SessionIDs) >= PromotionMinSessions
}

// PromotionLadder is the mechanical auto-promotion lane over a
// CandidateLedger: it records an observation and, when that observation
// carries the candidate across the Q-6 thresholds, promotes it.
//
// Mechanical means exactly that. No model is asked, no user is prompted,
// and nothing about the decision varies between runs: the same sequence of
// observations always produces the same promotions in the same order.
type PromotionLadder struct {
	ledger CandidateLedger
}

// NewPromotionLadder returns a ladder over ledger.
func NewPromotionLadder(ledger CandidateLedger) *PromotionLadder {
	return &PromotionLadder{ledger: ledger}
}

// Observe records one observation and promotes the candidate when that
// observation crosses the thresholds. It reports whether this call
// promoted, so a caller can act on the transition without re-deriving the
// rule.
//
// An observation that cannot be recorded promotes nothing: the error is
// returned and the door stays shut. Observing an already-promoted
// candidate is a no-op and promotes nothing again (R-14.22), which is what
// makes repeated calls safe.
func (p *PromotionLadder) Observe(
	ctx context.Context, obs Observation,
) (CandidateEntry, bool, error) {
	entry, err := p.ledger.Observe(ctx, obs)
	if err != nil {
		return CandidateEntry{}, false, err
	}
	if !ReadyForPromotion(entry) {
		return entry, false, nil
	}
	promoted, err := p.ledger.Promote(ctx, entry.Kind, entry.Name)
	if err != nil {
		return entry, false, err
	}
	return promoted, true, nil
}
