package memory

// Purpose: the weekly review digest event — the typed payload the
//   scheduled digest job publishes, the summary shape one candidate takes
//   inside it, and the sink it is offered to. It is the ONLY thing the
//   digest produces: the digest reports what the mechanical lane already
//   decided and changes nothing itself.
// Inputs: candidate views the review queue read, and the explicit window
//   they were read for.
// Outputs: one MemoryWeeklyDigest value per fire, offered to a sink.
// Constraints: the payload carries ADDRESSES, counts and statuses only —
//   never a record's description or body — because a bus event fans out to
//   every subscriber and a subscriber that asked to know a review is due
//   must not receive the text of what is being reviewed. The window is
//   carried in the payload rather than implied, so a reader can tell WHICH
//   week a digest speaks for; the thresholds are carried for the same
//   reason, so "below threshold" is checkable rather than asserted.
// SPORT: event:MemoryWeeklyDigest (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"time"
)

// MemoryWeeklyDigestEvent names the digest on the audit bus.
const MemoryWeeklyDigestEvent = "memory.review.weekly_digest"

// CandidateSummary is one candidate as the digest reports it.
//
// It is a deliberate subset of CandidateEntry: the address a reader needs
// in order to go and look, and the evidence the mechanical lane counted.
// No draft text appears here, and none may be added — see this file's
// Constraints note.
type CandidateSummary struct {
	// ID is the canonical "<kind>/<name>" address.
	ID string `json:"id"`
	// Kind is the candidate's taxonomy member.
	Kind MemoryKind `json:"kind"`
	// RefCount is how many references the ledger has counted.
	RefCount int `json:"ref_count"`
	// Sessions is how many DISTINCT sessions observed it. It is a count
	// rather than the identifiers themselves: a session identifier is not
	// something a reviewer acts on, and shipping the set would put
	// per-session identity on a fan-out bus for no reviewing purpose.
	Sessions int `json:"sessions"`
	// Status is the candidate's position on the ladder.
	Status CandidateStatus `json:"status"`
	// PromotedAt is when a promoted candidate was made durable, or nil.
	PromotedAt *time.Time `json:"promoted_at,omitempty"`
	// SnoozeUntil is when a deferred candidate becomes due again, or nil.
	SnoozeUntil *time.Time `json:"snooze_until,omitempty"`
}

// MemoryWeeklyDigest is the payload of one scheduled digest fire.
//
// It reports, it does not recommend. A pending candidate appears here
// because it is BELOW the mechanical threshold and therefore was not
// promoted; that is a fact about the count, not a suggestion that it
// should be promoted. MinRefCount and MinSessions travel with the payload
// so a reader can check that claim against the counts beside it rather
// than take it on trust.
//
// The contract names this type verbatim (S-14.T3 files_scope and the
// sport_updates row "event:MemoryWeeklyDigest"), and the CLI, the daemon
// wiring and the chat surface that consumes it all spell it this way;
// renaming it to WeeklyDigest to silence revive's stutter hint would break
// that.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type MemoryWeeklyDigest struct {
	// Since and Until are the closed-open window this digest speaks for,
	// both from the injected clock. Promoted candidates are included when
	// their promotion falls inside it; pending candidates are the queue's
	// state at Until. Nothing about the window is implied: a digest read
	// on its own says exactly which stretch of time it covers.
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// MinRefCount and MinSessions are the Q-6 thresholds in force when the
	// digest was built.
	MinRefCount int `json:"min_ref_count"`
	MinSessions int `json:"min_sessions"`
	// Pending are candidates still below the threshold and not snoozed, in
	// canonical address order.
	Pending []CandidateSummary `json:"pending"`
	// Promoted are candidates the mechanical lane promoted inside the
	// window, in canonical address order. They are listed so a promotion
	// can be disagreed with (`cascade memory review --revert`), which is
	// only possible if the digest says which records they were.
	Promoted []CandidateSummary `json:"promoted"`
	// Unreadable are the addresses of candidate records that could not be
	// read whole. They are named rather than dropped: a digest that
	// silently omits a damaged candidate reports a smaller queue than the
	// one on disk.
	Unreadable []string `json:"unreadable,omitempty"`
}

// EventName returns the bus name of this event.
func (MemoryWeeklyDigest) EventName() string { return MemoryWeeklyDigestEvent }

// DigestEventSink receives one digest per scheduled fire.
//
// It is declared here, at the point of use, for the reason
// ConsolidationEventSink gives: this package depends on the shape of a
// sink, not on any particular bus. A sink failure is reported to the job's
// caller — the digest itself changed nothing, so there is no half-done
// work to hide, and a fire nobody received is worth saying out loud.
type DigestEventSink interface {
	// MemoryWeeklyDigestReady reports one digest.
	MemoryWeeklyDigestReady(ctx context.Context, ev MemoryWeeklyDigest) error
}

// discardDigestEvents is the sink a digest job built with no sink uses. It
// is a real, complete implementation, not a placeholder: a caller with no
// event bus is a supported configuration, and discarding the event cannot
// change anything, because the digest writes nothing in the first place.
type discardDigestEvents struct{}

// MemoryWeeklyDigestReady discards the event.
func (discardDigestEvents) MemoryWeeklyDigestReady(context.Context, MemoryWeeklyDigest) error {
	return nil
}

// DiscardDigestEvents returns the no-op digest sink, for a caller that
// runs the digest job with no bus wired.
func DiscardDigestEvents() DigestEventSink { return discardDigestEvents{} }

// SummarizeCandidate projects a candidate view into the digest's summary
// shape. It lives here, beside the summary it produces, so the projection
// cannot drift from the payload contract, and it is the one place a field
// would have to be added — which is where the "addresses, never content"
// rule is easiest to keep.
func SummarizeCandidate(c CandidateEntry) CandidateSummary {
	return CandidateSummary{
		ID:          Address(c.Kind, c.Name),
		Kind:        c.Kind,
		RefCount:    c.RefCount,
		Sessions:    len(c.SessionIDs),
		Status:      c.Status,
		PromotedAt:  utcPtr(c.PromotedAt),
		SnoozeUntil: utcPtr(c.SnoozeUntil),
	}
}

// MemoryForgottenEvent names, on the audit bus, the retirement of one
// record at a user's explicit request.
const MemoryForgottenEvent = "memory.forgotten"

// ForgottenEvent is the payload the forget pipeline publishes once a
// record has been retired.
//
// It carries the ADDRESS, the instant and the stated reason, and never the
// record's description or body. A bus event fans out to every subscriber,
// and a subscriber that asked to know a record was forgotten must not
// receive, as the price of that notice, a copy of the thing the user just
// asked to be rid of. The same rule the weekly digest follows above, for
// the same reason, and it matters more here.
//
// Its first consumer is the backup and sync lane, which uses it to exclude
// the entity from an incremental export. That is why the event is emitted
// at all: without it, a forgotten record comes back at the next restore,
// which is not what a user who asked to forget something means.
type ForgottenEvent struct {
	// EntityID is the canonical "<kind>/<name>" address that was retired.
	EntityID string `json:"entity_id"`
	// Timestamp is when the retirement completed, from the injected clock.
	Timestamp time.Time `json:"timestamp"`
	// Reason is the caller's stated reason, verbatim and possibly empty.
	// It is the user's own words about their own record, not the record's
	// content.
	Reason string `json:"reason"`
}

// EventName returns the bus name of this event.
func (ForgottenEvent) EventName() string { return MemoryForgottenEvent }

// ForgetEventSink receives one event per completed retirement.
//
// It is declared here, at the point of use, for the reason
// ConsolidationEventSink gives. A sink failure is reported to the caller
// but does NOT fail the forget: by the time the event is offered the
// record is already gone, and refusing the call would tell a user their
// forget failed when it did not. The failure is carried in the outcome
// instead, so a caller can see that the backup lane was not told.
type ForgetEventSink interface {
	// MemoryForgotten reports one retirement.
	MemoryForgotten(ctx context.Context, ev ForgottenEvent) error
}

// discardForgetEvents is the sink a pipeline built with no sink uses. It
// is a real, complete implementation: a caller with no event bus is a
// supported configuration, and the outcome still says the event was not
// carried anywhere, so nothing is claimed that did not happen.
type discardForgetEvents struct{}

// MemoryForgotten discards the event.
func (discardForgetEvents) MemoryForgotten(context.Context, ForgottenEvent) error { return nil }

// DiscardForgetEvents returns the no-op forget sink, for a caller that
// runs the pipeline with no bus wired.
func DiscardForgetEvents() ForgetEventSink { return discardForgetEvents{} }
