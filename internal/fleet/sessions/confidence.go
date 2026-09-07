// Purpose: derive a bounded, deterministic ConfidenceScore from a set of
//
//	observation-derived Signals (HOW step 3).
//
// Inputs: a []Signal (statemachine.go's Advance builds it from an
//
//	Observation each call).
//
// Outputs: a ConfidenceScore in [ConfidenceNone, ConfidenceFull].
// Constraints: pure function - no I/O, no global state mutation (the
//
//	signalWeights table it reads is declared once in config.go and never
//	written to after init). Deterministic: identical signals always
//	produce the identical score. Fail-closed: a SignalKind with no
//	registered weight (including the SignalUnknown zero value) never
//	contributes weight and never causes an out-of-range result; a
//	repeated Kind is counted at most once, so a caller cannot inflate the
//	score by duplicating a Signal.
//
// SPORT: fleet/session-statemachine (ADD, per T-1 sport_updates).

// Package sessions doc: see domain.go for the canonical package comment.
package sessions

// ConfidenceScorer weighs a set of Signals into a single ConfidenceScore.
// The zero value is ready to use: it carries no state of its own.
type ConfidenceScorer struct{}

// NewConfidenceScorer returns a ready-to-use ConfidenceScorer.
func NewConfidenceScorer() *ConfidenceScorer {
	return &ConfidenceScorer{}
}

// Weigh sums the registered weight (config.go's signalWeights) of every
// present, recognized, non-duplicate Signal in signals. Because
// signalWeights' values sum to exactly ConfidenceFull (asserted by
// config_test.go), summing any subset of them can never exceed
// ConfidenceFull on its own; the explicit clamp below is a second,
// independent proof of that bound rather than the only thing enforcing
// it. A nil or empty signals slice returns ConfidenceNone.
func (ConfidenceScorer) Weigh(signals []Signal) ConfidenceScore {
	var total ConfidenceScore
	counted := make(map[SignalKind]bool, len(signals))
	for _, sig := range signals {
		if !sig.Present || counted[sig.Kind] {
			continue
		}
		weight, ok := signalWeights[sig.Kind]
		if !ok {
			// Unrecognized or unregistered kind (including the
			// SignalUnknown zero value): contributes zero, never a
			// silent upgrade.
			continue
		}
		counted[sig.Kind] = true
		total += weight
	}
	return clampConfidence(total)
}

// clampConfidence bounds score to [ConfidenceNone, ConfidenceFull]. Belt
// and suspenders alongside signalWeights summing to 1.0: this function
// alone is what makes "the score cannot exit its range for any input the
// machine accepts" true regardless of how signalWeights is edited later.
func clampConfidence(score ConfidenceScore) ConfidenceScore {
	switch {
	case score < ConfidenceNone:
		return ConfidenceNone
	case score > ConfidenceFull:
		return ConfidenceFull
	default:
		return score
	}
}
