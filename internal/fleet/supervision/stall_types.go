package supervision

// Purpose (this file): the stall-detector's own closed vocabulary
// (P1-E18-W4-S39-T5): StallKind (the five stall classifications the
// ticket names, plus StallKindUnknown — see the CONTRADICTION below),
// SignalKind (the three bus-normalized signal kinds R-16.73 names),
// StallSignal (one normalized observation), and StallEvent (the record a
// detection cycle produces).
//
// CONTRADICTION (StallKind has six members, not five). The ticket's task
// 1 names exactly "idle|blocked|oom_waiting|exit_unexpected|gate-denied".
// The AGENT-BRIEF's own discipline point for this ticket requires that
// "not seen recently" (a session IS being tracked and IS stale — a real,
// known idle stall) and "could not tell" (the tracker's event source is
// unavailable, so NOTHING about the session's progress can be asserted)
// report DIFFERENTLY, never as a silent healthy and never conflated with
// each other. Every other closed enum already touched by this phase that
// has to make this same distinction adds a sixth "unknown" member rather
// than reusing one of its real members for it (registry.LaneStateUnknown
// alongside four real states; nodes.PresenceUnknown alongside two real
// presences) — StallKindUnknown follows that exact, already-ratified
// precedent. It is carried only in a StallEvent's diagnostic StallKind
// field for observability; it is never a member of SignalKind (the wire
// classification R-16.73 actually closes at three) and the R-16.73
// fail-closed rule below is unchanged: an unrecognized SignalKind still
// maps to StallKindBlocked, never to StallKindUnknown, because that rule
// is about a malformed SIGNAL, not an unavailable SOURCE.
//
// SPORT: fleet.supervision.stall/ADDED (P1-E18-W4-S39-T5).

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// StallKind is the closed stall classification a StallEvent carries.
type StallKind string

// The six closed StallKind members. The zero value ("") is deliberately
// not a member, mirroring Kind's own convention in attention.go.
const (
	StallKindIdle           StallKind = "idle"
	StallKindBlocked        StallKind = "blocked"
	StallKindOOMWaiting     StallKind = "oom_waiting"
	StallKindExitUnexpected StallKind = "exit_unexpected"
	StallKindGateDenied     StallKind = "gate-denied"
	// StallKindUnknown reports that the detector could not tell whether a
	// tracked session is stalled at all, because its own event source
	// (the fleet.sessions.changed subscription) is unavailable. See this
	// file's CONTRADICTION above.
	StallKindUnknown StallKind = "unknown"
)

// Valid reports whether k is one of the six closed StallKind members.
func (k StallKind) Valid() bool {
	switch k {
	case StallKindIdle, StallKindBlocked, StallKindOOMWaiting, StallKindExitUnexpected, StallKindGateDenied, StallKindUnknown:
		return true
	}
	return false
}

// SignalKind is the closed, three-member vocabulary a normalized bus
// observation carries (R-16.73). It is deliberately a NARROWER, separate
// enum from StallKind: the two are related by signalToStallKind, never
// aliased, because a StallSignal describes what was OBSERVED on the bus
// and a StallEvent describes what the detector CONCLUDED from it — the
// ticket's own "the two enums are not the same enum" instruction.
type SignalKind string

// The three closed SignalKind members.
const (
	SignalBlocked     SignalKind = "blocked"
	SignalIdleTimeout SignalKind = "idle-timeout"
	SignalGateDenied  SignalKind = "gate-denied"
)

// Valid reports whether k is one of the three closed SignalKind members.
func (k SignalKind) Valid() bool {
	switch k {
	case SignalBlocked, SignalIdleTimeout, SignalGateDenied:
		return true
	}
	return false
}

// signalToStallKind maps a normalized signal to the stall classification
// it produces, per this ticket's NORMATIVE table. An unrecognized Kind is
// fail-closed to StallKindBlocked (R-16.73) — never silently dropped and
// never StallKindUnknown, which names a different failure (an
// unavailable source, not a malformed signal).
func signalToStallKind(k SignalKind) StallKind {
	switch k {
	case SignalBlocked:
		return StallKindBlocked
	case SignalIdleTimeout:
		return StallKindIdle
	case SignalGateDenied:
		return StallKindGateDenied
	default:
		return StallKindBlocked
	}
}

// StallSignal is one normalized observation delivered to
// Detector.Observe, from either the fleet.sessions.changed or the
// jobs.gate.denied bus stream (R-16.73).
type StallSignal struct {
	// Kind is the closed, three-member signal classification above.
	Kind SignalKind
	// SessionID names the session the signal is about. Required for
	// every Kind: escalation always acts on a session.
	SessionID string
	// JobID names the job a gate-denied signal was refused for. Required
	// only when Kind == SignalGateDenied; the 3-in-30-minute counting
	// rule is keyed on this field, not on SessionID.
	JobID string
	// At is the signal's own instant (unix millis), read from the
	// publisher's injected clock — never a bare time.Now at this layer.
	At int64
}

// Validate reports whether sig may be observed. Deliberately, Kind is
// NOT checked against SignalKind.Valid() here: R-16.73's own rule is
// that an unrecognized Kind is fail-closed to StallKindBlocked
// (signalToStallKind's default case), never refused outright — Validate
// only rejects what has no safe fail-closed interpretation at all: a
// missing SessionID (escalation always needs a target), or a missing
// JobID on a signal that claims to be a gate-denied one (the
// 3-in-30-minute rule has no key to count against without it).
func (sig StallSignal) Validate() error {
	if sig.SessionID == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSignal, "supervision: signal requires a session id")
	}
	if sig.Kind == SignalGateDenied && sig.JobID == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSignal, "supervision: gate-denied signal requires a job id")
	}
	return nil
}

// StallEvent is one detection cycle's conclusion: {session_id, stall_kind,
// stalled_since, elapsed_seconds}, per the ticket's full_desc.
type StallEvent struct {
	// SessionID names the stalled session.
	SessionID string
	// StallKind is the closed, six-member classification above.
	StallKind StallKind
	// StalledSince is the instant progress was last observed (unix
	// millis), or 0 when StallKind == StallKindUnknown (there is no
	// "since" to report when the source cannot say).
	StalledSince int64
	// ElapsedSeconds is how long the session has gone without progress,
	// as of the instant this event was produced. 0 when StallKind ==
	// StallKindUnknown.
	ElapsedSeconds int64
}

// Sentinel errors for this file's vocabulary. Both wrap exactly one
// frozen pkg/cascade.Kind (R-14.2).
var (
	// ErrInvalidSignal is returned by Validate for a signal that fails
	// the rules above.
	ErrInvalidSignal = cascade.New(cascade.KindInvalidInput, "supervision: invalid stall signal")
	// ErrSourceUnavailable is returned by ProgressTracker.Confidence when
	// the tracker's event source is unavailable — fail-closed input to
	// EscalationLadder.Advance's own fail-closed ConfidenceProvider
	// handling (a Confidence error is always treated as "stuck").
	ErrSourceUnavailable = cascade.New(cascade.KindUnavailable, "supervision: stall source unavailable")
)
