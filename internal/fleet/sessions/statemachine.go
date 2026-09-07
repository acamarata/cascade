// Purpose: the session observation state machine (HOW step 2): maps a
//
//	typed Observation and an injected clock to a (SessionState,
//	ConfidenceScore) pair.
//
// Inputs: an Observation (types.go) and a runtime.Clock (A-T4 testkit;
//
//	never a bare time.Now).
//
// Outputs: the classified SessionState and its ConfidenceScore.
// Constraints: deterministic (identical inputs -> identical outputs,
//
//	across GOOS and CPU count - there is no concurrency, randomness, or
//	wall-clock read inside Advance). Fail-closed: a nil clock or a
//	fully-zero Observation classifies as (StateUnknown, ConfidenceNone),
//	never a panic. TERMINAL STATES ARE TERMINAL: transitionTable's only
//	entry for StateClosed is StateClosed itself, so no signal combination
//	can move a closed session anywhere else (statemachine_test.go proves
//	this directly, not by re-deriving the same table). AN UNKNOWN OR
//	IMPOSSIBLE TRANSITION IS REFUSED, NOT DROPPED: when the observation's
//	candidate next state is not reachable from the prior stored state per
//	transitionTable, Advance returns the PRIOR state unchanged (with the
//	confidence the signals still support) rather than silently applying
//	the candidate or silently discarding the observation.
//
// SPORT: fleet/session-statemachine (ADD, per T-1 sport_updates).

// Package sessions doc: see domain.go for the canonical package comment.
package sessions

import (
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// transitionTable is the state machine's contract: the complete,
// hand-authored set of allowed (from -> to) edges. It is asserted
// directly against 12-QUALITY-CONSTITUTION.md's terminal-state and
// fail-closed requirements in statemachine_test.go, never against a
// second copy of itself. Every state has a self-loop (steady state is
// always reachable from itself) except that StateClosed's self-loop is
// its ONLY edge - the terminal-state requirement.
var transitionTable = map[SessionState]map[SessionState]bool{
	StateUnknown: {
		StateUnknown: true,
		StateActive:  true,
		StateClosed:  true,
	},
	StateActive: {
		StateActive:  true,
		StateIdle:    true,
		StateBlocked: true,
		StateClosed:  true,
	},
	StateIdle: {
		StateIdle:    true,
		StateActive:  true,
		StateBlocked: true,
		StateStalled: true,
		StateClosed:  true,
	},
	StateBlocked: {
		StateBlocked: true,
		StateActive:  true,
		StateStalled: true,
		StateClosed:  true,
	},
	StateStalled: {
		StateStalled: true,
		StateActive:  true,
		StateClosed:  true,
	},
	StateClosed: {
		StateClosed: true,
	},
}

// allowed reports whether transitionTable permits moving from current to
// candidate. A current state absent from the table (impossible for the
// closed SessionState enum, but checked rather than assumed) fails
// closed: no candidate is allowed.
func allowed(current, candidate SessionState) bool {
	edges, ok := transitionTable[current]
	if !ok {
		return false
	}
	return edges[candidate]
}

// StateMachine classifies Observations into SessionStates with a
// ConfidenceScore. The zero value is not usable; construct with
// NewStateMachine. StateMachine holds no mutable state of its own (its
// scorer is stateless too), so concurrent calls to Advance from multiple
// goroutines never share or race over anything (statemachine_test.go
// proves this under -race).
type StateMachine struct {
	scorer *ConfidenceScorer
}

// NewStateMachine returns a ready-to-use StateMachine.
func NewStateMachine() *StateMachine {
	return &StateMachine{scorer: NewConfidenceScorer()}
}

// Advance classifies obs as of clk.Now() and returns the resulting
// SessionState and its ConfidenceScore. See this file's header comment
// for the fail-closed, terminal-state, and refuse-not-drop guarantees.
func (m *StateMachine) Advance(obs Observation, clk runtime.Clock) (SessionState, ConfidenceScore) {
	if clk == nil {
		return StateUnknown, ConfidenceNone
	}
	current := currentState(obs)
	if current == StateClosed {
		// Terminal: no signal moves a closed session anywhere else.
		return StateClosed, ConfidenceFull
	}
	if obs.isZero() {
		return StateUnknown, ConfidenceNone
	}

	signals := buildSignals(obs, clk)
	confidence := m.scorer.Weigh(signals)
	candidate := classify(obs, signals, clk)

	if !allowed(current, candidate) {
		// Refused, not dropped: stay at the prior state rather than
		// silently applying an unreachable candidate or discarding the
		// observation outright.
		return current, confidence
	}
	return candidate, confidence
}

// currentState reads obs.Domain.State (this package's own opaque string,
// per domain.go's CONTRACT DEVIATION note) as the machine's prior state.
// A nil Domain, or a State value ParseSessionState does not recognize,
// fails closed to StateUnknown.
func currentState(obs Observation) SessionState {
	if obs.Domain == nil {
		return StateUnknown
	}
	st, ok := ParseSessionState(obs.Domain.State)
	if !ok {
		return StateUnknown
	}
	return st
}

// buildSignals derives the closed Signal set from obs as of now (per
// clk). Every registered SignalKind (config.go's signalWeights) is
// always present in the returned slice with Present set true/false, so
// Weigh's dedup logic never needs to distinguish "not observed" from
// "observed absent" - both are Present: false.
func buildSignals(obs Observation, clk runtime.Clock) []Signal {
	now := clk.Now()
	recentPrompt, recentTool, toolActivity := activitySignals(obs, now)
	return []Signal{
		{Kind: SignalProcessPresent, Present: obs.Census != nil},
		{Kind: SignalStreamOpen, Present: obs.Domain != nil && !obs.SSEClosed},
		{Kind: SignalTranscriptParseable, Present: obs.Transcript != nil && obs.TranscriptErr == nil},
		{Kind: SignalRecentPrompt, Present: recentPrompt},
		{Kind: SignalRecentTool, Present: recentTool},
		{Kind: SignalToolActivity, Present: toolActivity},
	}
}

// activitySignals reads the R-16.48 activity columns off obs.Domain (nil
// means none of the three fire) and reports whether a prompt/tool was
// observed within idleThreshold of now, plus whether any tool call has
// ever been recorded.
func activitySignals(obs Observation, now time.Time) (recentPrompt, recentTool, toolActivity bool) {
	if obs.Domain == nil {
		return false, false, false
	}
	recentPrompt = withinThreshold(now, obs.Domain.LastPromptAt, idleThreshold)
	recentTool = withinThreshold(now, obs.Domain.LastToolAt, idleThreshold)
	toolActivity = obs.Domain.ToolCount > 0
	return recentPrompt, recentTool, toolActivity
}

// withinThreshold reports whether tsMillis (a *int64 unix-millis
// activity column, nil meaning "never recorded") lies within threshold
// of now. A future timestamp (clock skew) is treated as within
// threshold rather than computing a negative elapsed duration.
func withinThreshold(now time.Time, tsMillis *int64, threshold time.Duration) bool {
	if tsMillis == nil {
		return false
	}
	ts := time.UnixMilli(*tsMillis)
	if now.Before(ts) {
		return true
	}
	return now.Sub(ts) <= threshold
}

// classify derives the candidate next SessionState from obs and its
// pre-built signals. Called only after Advance has already handled the
// terminal-state and fully-zero-Observation cases, so obs here always
// carries at least one non-zero field.
func classify(obs Observation, signals []Signal, clk runtime.Clock) SessionState {
	if obs.Domain != nil && obs.Census == nil {
		// The domain knows this session but its process is gone: the
		// session ended.
		return StateClosed
	}
	if hasPresentSignal(signals, SignalRecentPrompt) || hasPresentSignal(signals, SignalRecentTool) {
		return StateActive
	}
	if obs.Domain == nil {
		// No domain record and no recent activity signal: nothing to
		// classify a steady state from.
		return StateUnknown
	}
	return classifyByElapsed(obs.Domain, clk.Now())
}

// classifyByElapsed maps "time since the most recent recorded activity"
// to Idle/Blocked/Stalled, per config.go's three thresholds. Called only
// when neither recent-prompt nor recent-tool fired, so an elapsed
// duration within idleThreshold here means SOME activity timestamp
// exists but is not the freshest kind of activity (e.g. an
// InstructionsLoadedAt only) - still classified as Idle, not Active,
// since Active is earned solely by the recent-prompt/recent-tool
// signals above.
func classifyByElapsed(rec *SessionRecord, now time.Time) SessionState {
	last := lastActivity(rec)
	if last == nil {
		return StateIdle
	}
	elapsed := now.Sub(*last)
	switch {
	case elapsed <= blockedThreshold:
		return StateIdle
	case elapsed <= stalledThreshold:
		return StateBlocked
	default:
		return StateStalled
	}
}

// lastActivity returns the most recent of rec's three *_at activity
// timestamps, or nil when none were ever recorded.
func lastActivity(rec *SessionRecord) *time.Time {
	var latest *int64
	for _, ts := range []*int64{rec.InstructionsLoadedAt, rec.LastPromptAt, rec.LastToolAt} {
		if ts == nil {
			continue
		}
		if latest == nil || *ts > *latest {
			latest = ts
		}
	}
	if latest == nil {
		return nil
	}
	t := time.UnixMilli(*latest)
	return &t
}

// hasPresentSignal reports whether signals contains kind with Present
// true.
func hasPresentSignal(signals []Signal, kind SignalKind) bool {
	for _, sig := range signals {
		if sig.Kind == kind && sig.Present {
			return true
		}
	}
	return false
}
