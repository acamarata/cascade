package review

// Purpose: the write half of the review queue — the four actions a human
//   can take on a candidate, and the audit event each one emits.
// Inputs: a canonical "<kind>/<name>" address and one action name, both
//   from an untrusted peer.
// Outputs: the candidate's state after the action, or a typed pkg/cascade
//   error.
// Constraints: an action is EXPLICIT. Nothing here runs as a side effect
//   of listing or of building a digest, and each call carries out exactly
//   one action on exactly one named candidate. An unknown action or an
//   unknown address is refused rather than interpreted.
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The four review actions.
const (
	// ActionApprove promotes a below-threshold candidate early, ahead of
	// the mechanical threshold.
	ActionApprove = "approve"
	// ActionSkip leaves a candidate exactly as it is. It is a recorded
	// no-op: the audit trail gains the fact that a human looked and chose
	// nothing, and the ledger gains nothing at all.
	ActionSkip = "skip"
	// ActionDefer hides a pending candidate until a later instant.
	ActionDefer = "defer"
	// ActionRevert takes back a promotion.
	ActionRevert = "revert"
)

// DefaultDeferDays is how long a defer hides a candidate when the caller
// names no number of days. A week matches the digest cadence: the default
// deferral brings a candidate back with the next digest rather than at
// some instant unrelated to when anyone next looks.
const DefaultDeferDays = 7

// maxDeferDays caps a deferral. A candidate deferred past this is
// effectively hidden forever, which is a decision the queue must not let a
// caller take by accident under the name "defer"; forgetting a candidate
// is the forget pipeline's job and says so.
const maxDeferDays = 365

// Review action sentinel errors.
var (
	// ErrUnknownAction is returned for an action outside the four names.
	ErrUnknownAction = cascade.New(cascade.KindInvalidInput,
		"unknown review action")
	// ErrInvalidDeferDays is returned for a deferral that is not a
	// positive number of days within the cap.
	ErrInvalidDeferDays = cascade.New(cascade.KindInvalidInput,
		"invalid defer window")
)

// revertReason is the reason recorded on a revert made from this surface.
// It is fixed rather than caller-supplied: ActParams is the external-input
// shape this ticket ratifies, and a free-text field on it would be a
// second place a peer's bytes reach a stored record.
const revertReason = "reverted from the review queue"

// ActionEvent names one action a human took. It carries the
// address, the action and the instant — never the record's content, for
// the reason given in internal/memory/events.go.
type ActionEvent struct {
	// ID is the canonical address the action was taken on.
	ID string `json:"id"`
	// Kind is the candidate's taxonomy member.
	Kind memory.MemoryKind `json:"kind"`
	// Action is one of the four action names.
	Action string `json:"action"`
	// Changed reports whether the ledger changed. A skip is recorded with
	// Changed false, so an audit reader can tell "a human looked and left
	// it" from "a human acted".
	Changed bool `json:"changed"`
	// At is the instant of the action, from the injected clock.
	At time.Time `json:"at"`
}

// EventName returns the bus name of this event.
func (ActionEvent) EventName() string { return ActionEventName }

// ActionEventName names review actions on the audit bus.
const ActionEventName = "memory.review.action"

// ActionEventSink receives one event per review action.
//
// It is declared at the point of use for the reason internal/memory's
// sinks are. A sink failure is returned to the caller: the ledger change
// is already durable by then, and reporting an action nobody recorded
// would be the one thing this surface exists to prevent.
type ActionEventSink interface {
	// ReviewActed reports one action.
	ReviewActed(ctx context.Context, ev ActionEvent) error
}

// discardActionEvents is the sink a queue built with no sink uses. It is a
// real, complete implementation: a caller with no bus is supported, and
// discarding an event never changes what the ledger stores.
type discardActionEvents struct{}

// ReviewActed discards the event.
func (discardActionEvents) ReviewActed(context.Context, ActionEvent) error { return nil }

// ActParams is memory.review.act's input.
type ActParams struct {
	// ID is the canonical "<kind>/<name>" address to act on.
	ID string `json:"id"`
	// Action is one of approve, skip, defer, revert.
	Action string `json:"action"`
	// DeferDays is how many days a defer hides the candidate. Zero means
	// DefaultDeferDays; it is ignored by every other action.
	DeferDays int `json:"defer_days,omitempty"`
}

// ActResult is memory.review.act's output.
type ActResult struct {
	// Action is the action that was carried out.
	Action string `json:"action"`
	// Changed reports whether the ledger changed.
	Changed bool `json:"changed"`
	// Item is the candidate's state AFTER the action, so a caller reads
	// what it now is rather than what it was asked to become.
	Item memory.CandidateSummary `json:"item"`
	// At is the instant of the action, from the injected clock.
	At time.Time `json:"at"`
}

// Act carries out exactly one review action on one candidate.
//
// Every path here is explicit and addressed. There is no bulk mode and no
// "apply the obvious thing": a caller that wants four candidates approved
// says so four times, because a single call that acted on a set the caller
// never named is how a review surface turns into an automated one.
func (q *Queue) Act(ctx context.Context, p ActParams) (ActResult, error) {
	kind, name, err := memory.ParseAddress(p.ID)
	if err != nil {
		return ActResult{}, err
	}
	now := q.clock.Now().UTC()
	entry, changed, err := q.apply(ctx, kind, name, p, now)
	if err != nil {
		return ActResult{}, err
	}
	result := ActResult{
		Action:  p.Action,
		Changed: changed,
		Item:    memory.SummarizeCandidate(entry),
		At:      now,
	}
	return result, q.sink.ReviewActed(ctx, ActionEvent{
		ID: result.Item.ID, Kind: kind, Action: p.Action, Changed: changed, At: now,
	})
}

// apply is the action switch, split from Act so both stay inside the
// 50-line function cap. It is the ONE place an action name becomes a
// ledger call, so no action can grow a second, divergent implementation.
func (q *Queue) apply(
	ctx context.Context, kind memory.MemoryKind, name string, p ActParams, now time.Time,
) (memory.CandidateEntry, bool, error) {
	switch p.Action {
	case ActionApprove:
		entry, err := q.ledger.Promote(ctx, kind, name)
		return entry, err == nil, err
	case ActionSkip:
		// A skip reads the candidate and writes nothing. The read is what
		// makes an unknown address a refusal rather than a silent success:
		// a caller must not be told it skipped something that is not there.
		entry, err := q.ledger.Get(ctx, kind, name)
		return entry, false, err
	case ActionDefer:
		days, err := deferDays(p.DeferDays)
		if err != nil {
			return memory.CandidateEntry{}, false, err
		}
		entry, err := q.ledger.Defer(ctx, kind, name, now.AddDate(0, 0, days))
		return entry, err == nil, err
	case ActionRevert:
		entry, err := q.ledger.Revert(ctx, kind, name, revertReason)
		return entry, err == nil, err
	default:
		return memory.CandidateEntry{}, false, cascade.Wrapf(cascade.KindInvalidInput,
			ErrUnknownAction, "unknown review action %q, want one of %s, %s, %s, %s",
			p.Action, ActionApprove, ActionSkip, ActionDefer, ActionRevert)
	}
}

// deferDays resolves and bounds a deferral window.
func deferDays(n int) (int, error) {
	if n == 0 {
		return DefaultDeferDays, nil
	}
	if n < 0 || n > maxDeferDays {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidDeferDays,
			"defer window is %d days, want 1 to %d", n, maxDeferDays)
	}
	return n, nil
}
