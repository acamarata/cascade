// Purpose: the state vocabulary and observation aggregate the L/S-25.T1
//
//	state machine (statemachine.go) and confidence scorer (confidence.go)
//	are built on. R-14.41 gives this ticket sole ownership of the state
//	vocabulary; domain.go's SessionRecord.State stores this package's
//	SessionState values as an opaque string and defines no graph of its
//	own.
//
// Inputs: none (this file is pure type/constant declarations).
// Outputs: SessionState, Observation, ConfidenceScore, Signal/SignalKind.
// Constraints: OWNER PREREQ NOT MET (recorded, not papered over). The
//
//	ticket's HOW step 1 requires state labels to come from an
//	owner-provided labeled corpus (06-FORGE-SPEC §5.12, owner prereq #5).
//	As of this ticket's run, internal/fleet/sessions/testdata/corpus/ is
//	empty (see that directory's README.md) - no corpus was deposited.
//	Task 1 anticipates exactly this case ("if absent, create the
//	directory and a placeholder README that causes TestSessionStateAccuracy
//	to skip cleanly"), so the six states below are a CHOSEN interim
//	vocabulary, not corpus-derived, built from the only evidence actually
//	available: the S-24 observation channels this package already reads
//	(census presence, transcript parseability, and the R-16.48 activity
//	columns) plus the R/S-39.T1 seam requirement that the label set
//	include blocked and stalled. This is filed as an Art.9 defect against
//	the corpus owner_prereq in this ticket's journal; the vocabulary may
//	need relabeling once the real corpus lands, which is exactly why
//	ParseSessionState fails closed on any name outside this set rather
//	than accepting arbitrary strings.
//
// SPORT: fleet/session-statemachine (ADD, per T-1 sport_updates).

// Package sessions doc: see domain.go for the canonical package comment.
package sessions

import (
	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/tailer"
)

// SessionState classifies a live fleet session's observed lifecycle
// position. See this file's header comment: this set is a chosen interim
// vocabulary (owner corpus not yet deposited), not corpus-derived.
type SessionState int

const (
	// StateUnknown is the zero value and the fail-closed sentinel: no
	// basis for classification (nil/zero Observation, an unparseable
	// prior state, or an Observation with a stale process and no domain
	// record to explain it). Never a permissive default.
	StateUnknown SessionState = iota
	// StateActive reports recent prompt or tool activity within
	// idleThreshold of the observation instant.
	StateActive
	// StateIdle reports a live, tracked process with no activity within
	// idleThreshold but within blockedThreshold.
	StateIdle
	// StateBlocked reports no activity within blockedThreshold but within
	// stalledThreshold. Consumed by R/S-39.T1's attention queue.
	StateBlocked
	// StateStalled reports no activity beyond stalledThreshold, with the
	// process still present. Consumed by R/S-39.T1's attention queue.
	StateStalled
	// StateClosed is TERMINAL: the process is gone and a domain record
	// existed. No event moves a session out of StateClosed (see
	// statemachine.go's transitionTable).
	StateClosed
)

// String returns state's stable lowercase name, used as the opaque string
// domain.go's SessionRecord.State persists.
func (s SessionState) String() string {
	switch s {
	case StateUnknown:
		return "unknown"
	case StateActive:
		return "active"
	case StateIdle:
		return "idle"
	case StateBlocked:
		return "blocked"
	case StateStalled:
		return "stalled"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// sessionStateNames is ParseSessionState's fail-closed lookup table: the
// single source of truth for valid names, so a corpus label typo (or a
// future corpus using a name outside this set) is refused rather than
// silently accepted as a new state.
var sessionStateNames = map[string]SessionState{
	"unknown": StateUnknown,
	"active":  StateActive,
	"idle":    StateIdle,
	"blocked": StateBlocked,
	"stalled": StateStalled,
	"closed":  StateClosed,
}

// ParseSessionState resolves name (case-sensitive, matching String's
// output) to its SessionState. An unrecognized name fails closed: it
// returns (StateUnknown, false), never a silent upgrade to any other
// state and never a panic.
func ParseSessionState(name string) (SessionState, bool) {
	st, ok := sessionStateNames[name]
	return st, ok
}

// ConfidenceScore is a scalar classification confidence, always in the
// closed range [0.0, 1.0].
type ConfidenceScore float64

const (
	// ConfidenceNone is the 0.0 sentinel: no basis for classification.
	ConfidenceNone ConfidenceScore = 0.0
	// ConfidenceFull is the 1.0 sentinel: every registered signal fired.
	ConfidenceFull ConfidenceScore = 1.0
)

// SignalKind identifies one observation-derived input to the confidence
// scorer. The set is closed; an unrecognized SignalKind (e.g. a future
// caller's typo, or a stale value from a removed kind) contributes zero
// weight rather than being silently treated as any existing kind - see
// confidence.go's Weigh.
type SignalKind int

const (
	// SignalUnknown is the zero value; ConfidenceScorer.Weigh never
	// assigns it a registered weight, so a zero-value Signal always
	// contributes zero (fail-closed).
	SignalUnknown SignalKind = iota
	// SignalProcessPresent reports that census.Snapshot found the
	// session's tracked process.
	SignalProcessPresent
	// SignalRecentPrompt reports a prompt within idleThreshold, from the
	// sessions domain's LastPromptAt activity column.
	SignalRecentPrompt
	// SignalRecentTool reports a tool call within idleThreshold, from the
	// sessions domain's LastToolAt activity column.
	SignalRecentTool
	// SignalToolActivity reports that the session has ever recorded a
	// tool call (ToolCount > 0).
	SignalToolActivity
	// SignalTranscriptParseable reports that the most recent tailer read
	// produced a Record with no *tailer.ParseError.
	SignalTranscriptParseable
	// SignalStreamOpen reports that the sessions domain's SSE stream has
	// not been observed closed for this session.
	SignalStreamOpen
)

// Signal is one weighed input to ConfidenceScorer.Weigh: whether a given
// observation-derived condition fired.
type Signal struct {
	Kind    SignalKind
	Present bool
}

// Observation aggregates the four S-24 typed channels the state machine
// classifies from. Every field is independently optional (nil/zero
// means "not observed this cycle"): Advance is fail-closed over any
// combination, including the fully zero value.
type Observation struct {
	// Census is the S-24.T1 process census snapshot for this session's
	// tracked process, or nil when no matching process was found.
	Census *census.Snapshot
	// Transcript is the most recent S-24.T2 tailer.Record decoded for
	// this session, or nil when none was read this cycle.
	Transcript *tailer.Record
	// TranscriptErr is non-nil when the most recent tailer read failed
	// (e.g. a truncated line, surfaced by tailer as *tailer.ParseError)
	// rather than producing a Transcript. Mutually informative with
	// Transcript being nil; both may be nil when nothing was read.
	TranscriptErr error
	// Domain is the S-24.T3 sessions domain SessionRecord for this
	// session - carrying both the domain open/close/update state and the
	// R-16.48 hook-derived activity columns (S-24.T4 writes them; this
	// package reads them here, never by importing hookpacks - see the
	// ticket's HOW note). nil means no domain record exists yet.
	Domain *SessionRecord
	// SSEClosed reports that the daemon has observed its SSE stream to
	// this session's subscriber close (a distinct condition from Domain
	// being nil: a session can have a domain record and a closed stream
	// simultaneously, e.g. a client disconnect that has not yet been
	// followed by a domain close write).
	SSEClosed bool
}

// isZero reports whether obs carries no observation at all: the
// fail-closed case Advance maps to StateUnknown/ConfidenceNone.
func (obs Observation) isZero() bool {
	return obs.Census == nil &&
		obs.Transcript == nil &&
		obs.TranscriptErr == nil &&
		obs.Domain == nil &&
		!obs.SSEClosed
}
