// Purpose (this file): the ONE home of the R-21.175 expected-time formula
// and its hysteresis comparison (R-16.71). ReworkCyclesEst and
// JumpHysteresisFactor are NOT redeclared here -- they are this package's
// own priors.go constants (S-63.T4), read by name, never re-literalled.
//
// Inputs: queue_wait, duration_est (from the injected EstimateSource) and
// a capability score (from the injected CapabilityScorer, via
// PReworkFromScore).
// Outputs: ExpectedTime and JumpFires's boolean.
// Constraints: pure functions; no bare time.Now; no inline literals for
// the rework-cycle count or the hysteresis margin.
//
// SPORT: fleet.capacity.expected_time (ADD, P1-E31-W6-S63-T2).

package capacity

import "time"

// ExpectedTime carries the R-21.175 formula's inputs and result for one
// tier, so the jump decision and every number it used are reproducible
// (consumed by explain.go's TierSelection.estimates and by `cascade fleet
// learn explain`, AE/S-64.T4).
type ExpectedTime struct {
	QueueWait       time.Duration
	DurationEst     time.Duration
	PRework         float64
	ReworkCyclesEst int
	ExpectedTime    time.Duration
}

// ComputeExpectedTime returns the R-21.175 expected_time for one tier:
//
//	expected_time = queue_wait + duration_est * (1 + p_rework * rework_cycles_est)
//
// with p_rework = 1 - score (PReworkFromScore, priors.go) and
// rework_cycles_est = ReworkCyclesEst (priors.go) -- no inline literal.
func ComputeExpectedTime(queueWait, durationEst time.Duration, score float64) ExpectedTime {
	pRework := PReworkFromScore(score)
	factor := 1 + pRework*float64(ReworkCyclesEst)
	et := queueWait + time.Duration(float64(durationEst)*factor)
	return ExpectedTime{
		QueueWait:       queueWait,
		DurationEst:     durationEst,
		PRework:         pRework,
		ReworkCyclesEst: ReworkCyclesEst,
		ExpectedTime:    et,
	}
}

// JumpFires reports whether tier0's expected time trips the R-21.175
// expected-time jump against bestLower, the best available lower tier's
// expected time, using the JumpHysteresisFactor margin (priors.go):
//
//	fires  <=>  expected_time(tier0) < JumpHysteresisFactor * expected_time(bestLower)
//
// Strictly-less: a tier0 estimate exactly AT the hysteresis boundary does
// not fire, so a score hovering at the margin never oscillates call to
// call (the margin's whole purpose).
func JumpFires(tier0, bestLower ExpectedTime) bool {
	return float64(tier0.ExpectedTime) < JumpHysteresisFactor*float64(bestLower.ExpectedTime)
}
