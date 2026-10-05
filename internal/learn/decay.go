package learn

// Purpose: the exponential-decay helper for capability scoring (R-16.37
//   §Fleet capacity, R-16.18). Every stored (alpha, beta, last_updated) row
//   is age-adjusted at READ time and again at every WRITE, so old
//   observations fade with a 30-day half-life instead of pinning a score.
// Inputs: an age (now minus last_updated) and a stored mass.
// Outputs: a weight in (0, 1] and the decayed mass.
// Constraints: no bare time.Now (the caller passes both instants, taken from
//   an injected clock); a future last_updated (clock skew, a restored
//   backup) has age zero and weighs exactly 1.0, never more: decay never
//   amplifies.
// SPORT: internal.learn.decayWeight/ADDED (P1-CAP-03).

import (
	"math"
	"time"
)

// halfLife is the capability-score half-life: an observation this old weighs
// half of its face value.
const halfLife = 30 * 24 * time.Hour

// decayWeight returns the multiplier for an observation of the given age:
// 2^(-age/halfLife), which equals exp(-ln(2) * age_days / 30). A zero or
// negative age (a future timestamp) weighs 1.0.
func decayWeight(age time.Duration) float64 {
	if age <= 0 {
		return 1.0
	}
	return math.Exp2(-float64(age) / float64(halfLife))
}

// decayed scales mass by decayWeight(now - last).
func decayed(mass float64, last, now time.Time) float64 {
	return mass * decayWeight(now.Sub(last))
}
