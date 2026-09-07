// Purpose: the state machine's tunable table - signal weights and
//
//	activity thresholds - registered once, at package init, rather than
//	hardcoded inside statemachine.go's Advance (HOW step 3: "signal
//	weights are registered at package init from a config table, not
//	hardcoded inside Advance").
//
// Inputs: none (literal declarations).
// Outputs: signalWeights (confidence.go's Weigh reads it) and the three
//
//	activity thresholds (statemachine.go's classify reads them).
//
// Constraints: signalWeights' values MUST sum to exactly ConfidenceFull
//
//	(1.0) - config_test.go asserts this - so Weigh's sum-of-present-weights
//	construction is bounded to [0.0, 1.0] by the table itself, not by a
//	runtime clamp alone. CHOSEN, not corpus-derived (see types.go's header
//	comment): the owner-provided corpus this ticket depends on to derive
//	real weights was not deposited: internal/fleet/sessions/testdata/corpus/
//	is empty. These weights give every registered signal a share
//	proportional to how directly it evidences "the session is currently
//	active" (the only classification decision this scorer makes), and are
//	deliberately conservative pending real labeled data.
//
// SPORT: fleet/session-statemachine (ADD, per T-1 sport_updates).

// Package sessions doc: see domain.go for the canonical package comment.
package sessions

import "time"

// signalWeights is the closed weight table ConfidenceScorer.Weigh sums
// over. A SignalKind absent from this map (including SignalUnknown)
// contributes zero weight - fail-closed for any unrecognized kind.
var signalWeights = map[SignalKind]ConfidenceScore{
	SignalRecentPrompt:        0.30,
	SignalRecentTool:          0.25,
	SignalProcessPresent:      0.15,
	SignalTranscriptParseable: 0.15,
	SignalStreamOpen:          0.10,
	SignalToolActivity:        0.05,
}

// Activity thresholds classify.go's classify uses to turn "time since
// last observed activity" into a candidate SessionState. Each threshold
// is an upper bound: activity within idleThreshold is StateActive
// territory (handled directly by the recent-prompt/recent-tool signals,
// not by elapsed time), between idleThreshold and blockedThreshold is
// StateIdle, between blockedThreshold and stalledThreshold is
// StateBlocked, and beyond stalledThreshold is StateStalled.
const (
	idleThreshold    = 2 * time.Minute
	blockedThreshold = 10 * time.Minute
	stalledThreshold = 30 * time.Minute
)
