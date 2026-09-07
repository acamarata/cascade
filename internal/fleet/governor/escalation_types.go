// Package governor (escalation_types.go) defines the escalation ladder's
// rung enumeration, per-rung policy, and journaled event vocabulary
// (P1-E13-W3-S27-T3).
//
// Purpose: EscalationRung (the four-rung enum: Retry, Context,
//
//	SupervisorTask, Human), safeRung (the fail-closed mapper any
//	unrecognised or out-of-range rung value collapses to), EscalationPolicy
//	(per-rung attempt budgets, inter-rung delay, and the confidence
//	threshold that triggers Advance), and EscalationEvent (the record
//	escalation.go appends to the S-27.T1 JournalStore at every rung
//	transition).
//
// Inputs: none directly; these types are populated by escalation.go's
//
//	EscalationLadder.Advance and decoded back out of journal.Entry.Payload.
//
// Outputs: EscalationEvent values are marshaled to JSON for the journal and
//
//	unmarshaled back on the next Advance call.
//
// Constraints: EscalationRung's zero value is deliberately not a member of
//
//	the enum (mirrors journal.Kind's own "zero is invalid" convention) so a
//	forgotten field reads as a bug rather than silently meaning Retry.
//	safeRung is the ONLY definition of "valid rung" and is fail-closed:
//	every value outside the four ratified rungs, including the zero value
//	and any value past Human, maps to Human (05.15's classifier
//	fail-closed pattern). No bare time.Now: EscalationEvent.Timestamp is
//	always stamped from escalation.go's injected runtime.Clock.
//
// SPORT: internal/fleet/governor.EscalationLadder (ADD, per T-3
//
//	sport_updates).
package governor

import "time"

// EscalationRung is one rung of the four-rung escalation ladder, in the
// order Advance walks them. Declared as a defined type so a raw int cannot
// be passed where a rung is expected without an explicit conversion.
type EscalationRung int

// The four ratified rungs, in ladder order. The zero value is deliberately
// not a member (see safeRung), and RungHuman is the ladder's terminal rung:
// safeRung never returns a value past it.
const (
	_ EscalationRung = iota // 0 is deliberately not a valid rung

	// RungRetry retries the stuck work automatically, with no additional
	// context or human involvement. The ladder's first rung.
	RungRetry
	// RungContext enriches the entity with additional context before
	// retrying, one rung past RungRetry.
	RungContext
	// RungSupervisorTask creates a supervisor task to monitor the entity,
	// one rung past RungContext.
	RungSupervisorTask
	// RungHuman notifies a human operator. The ladder's terminal rung:
	// nothing escalates past it, and safeRung maps every out-of-range or
	// unrecognised value to it (fail closed, mirrors 06-FORGE-SPEC.md
	// §5.15's classifier rule).
	RungHuman
)

// rungNames holds the display string for each valid rung, indexed by rung
// value. Index 0 is the invalid zero value's placeholder.
var rungNames = [...]string{
	"",
	"retry",
	"context",
	"supervisor-task",
	"human",
}

// String returns the rung's stable lowercase-hyphenated name.
func (r EscalationRung) String() string {
	if r < RungRetry || r > RungHuman {
		return "invalid-rung"
	}
	return rungNames[r]
}

// safeRung is the ladder's single fail-closed mapper: every one of the four
// ratified rungs maps to itself, and every other value — the zero value,
// a negative value, or anything past RungHuman (in particular
// RungHuman+1, which is how Advance's own "one rung past the current one"
// arithmetic asks for "the rung after Human") — maps to RungHuman. This is
// simultaneously the enum's validity check and the ladder's proof of
// termination: advancing from any rung, valid or not, can only ever land
// on one of the four rungs, and RungHuman is a fixed point of safeRung, so
// no sequence of advances can produce a fifth rung or cycle back to an
// earlier one.
func safeRung(r EscalationRung) EscalationRung {
	switch r {
	case RungRetry, RungContext, RungSupervisorTask, RungHuman:
		return r
	default:
		return RungHuman
	}
}

// EscalationPolicy configures one EscalationLadder: how many attempts each
// rung is allowed before Advance forces the next one (R-21.216), how long a
// caller should wait between Advance calls, and the confidence level below
// which Advance treats the entity as stuck and does work.
type EscalationPolicy struct {
	// MaxAttempts caps the number of Advance calls that may execute a
	// given rung's seam before the ladder advances to the next rung
	// regardless of that seam's own outcome (R-21.216). A rung missing
	// from this map reads as a zero cap: the very first Advance at that
	// rung both executes it and immediately becomes eligible to advance
	// on the next call, which is the fail-closed default for a caller
	// that forgot to configure a rung's budget.
	MaxAttempts map[EscalationRung]int
	// RungDelay is the minimum interval a caller should wait between
	// successive Advance calls for the same entity. Advance itself does
	// not sleep or enforce this — 02-TARGET-STRUCTURE.md's clock-injection
	// amendment forbids blocking domain logic on a timer — it is data for
	// whatever scheduler outside this ticket's scope drives Advance on a
	// cadence.
	RungDelay time.Duration
	// ConfidenceThreshold is the [0,1] confidence level at or above which
	// Advance treats the entity as no longer stuck and returns nil without
	// executing any rung.
	ConfidenceThreshold float64
}

// EscalationEvent is one rung transition, appended to the S-27.T1
// JournalStore under journal.KindEscalation at every Advance call that does
// work. Attempt counts how many times Rung's seam has been executed
// consecutively without an intervening advance; a fresh arrival at a rung
// (whether the ladder's first rung or one reached by an advance) always
// records Attempt starting at 1.
type EscalationEvent struct {
	// EntityID names the stuck task or session this event describes.
	EntityID string `json:"entity_id"`
	// Rung is the rung this event reflects: either the rung whose seam
	// just ran (on success or on attempt-exhaustion carrying no seam
	// failure), or the rung the ladder just advanced to (on seam failure).
	Rung EscalationRung `json:"rung"`
	// Attempt is the number of consecutive Advance calls, including this
	// one, that have executed Rung's seam without an intervening advance.
	Attempt int `json:"attempt"`
	// LastError is the seam failure that caused this event, or empty on a
	// success or attempt-exhaustion event.
	LastError string `json:"last_error,omitempty"`
	// Timestamp is when this event was recorded, read from the ladder's
	// injected runtime.Clock (never a bare time.Now, Art.7.3).
	Timestamp time.Time `json:"timestamp"`
}
