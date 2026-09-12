// Purpose: R-21.116's MANDATORY, closed dimension-to-window map and
//
//	TimeToReset, so a windowLength is never zero or absent and no
//	pressure computation can divide by zero. WindowFor also refuses the
//	two R-21.116 batch gauges (concurrent_requests, enqueued_tokens):
//	they gate eligibility only and must never reach a pressure formula.
//
// Inputs: a dimension name. Outputs: (BucketWindow, windowLength) or
//
//	ErrTopologyInvariant.
//
// Constraints: the map is CLOSED -- an unmapped dimension name is refused,
//
//	never defaulted to a zero windowLength.
//
// SPORT: fleet/topology/window_map/ADD (P1-E40-W9-S77-T3).

package topology

import "time"

// windowSpec is one dimension's (window kind, window length) pair.
type windowSpec struct {
	Window BucketWindow
	Length time.Duration
}

// windowMap is R-21.116's mandatory, closed dimension -> window map.
var windowMap = map[string]windowSpec{
	DimensionRPM: {BucketWindowRolling, 60 * time.Second},
	DimensionTPM: {BucketWindowRolling, 60 * time.Second},
	DimensionRPD: {BucketWindowDay, 24 * time.Hour},

	DimensionSession5h: {BucketWindow5h, 5 * time.Hour},
	DimensionWindow5h:  {BucketWindow5h, 5 * time.Hour},

	DimensionWeeklyShared:        {BucketWindowWeek, 7 * 24 * time.Hour},
	DimensionWeeklyModelFraction: {BucketWindowWeek, 7 * 24 * time.Hour},
	DimensionWeekly:              {BucketWindowWeek, 7 * 24 * time.Hour},

	DimensionMonthly: {BucketWindowMonth, 30 * 24 * time.Hour},
}

// gaugeNames is the set WindowFor refuses outright: a batch gauge gates
// eligibility only and must never reach a pressure computation.
var gaugeNames = map[string]bool{
	GaugeConcurrentRequests: true,
	GaugeEnqueuedTokens:     true,
}

// WindowFor returns dimension's mandatory (window kind, window length).
// A batch gauge name or any name absent from the closed map is
// ErrTopologyInvariant -- WindowFor never returns a zero windowLength.
func WindowFor(dimension string) (BucketWindow, time.Duration, error) {
	if gaugeNames[dimension] {
		return "", 0, newInvariantErr("window_map", dimension, "batch gauges gate eligibility only and are never a pressure input")
	}
	spec, ok := windowMap[dimension]
	if !ok {
		return "", 0, newInvariantErr("window_map", dimension, "dimension has no entry in the mandatory window map")
	}
	return spec.Window, spec.Length, nil
}

// TimeToReset computes how long until b's window resets, at instant now.
// For a rolling window (rpm, tpm), windowLength IS the rolling period and
// the result is windowLength * b.RemainingFraction (R-21.116's own
// formula). For a fixed window, the result is b.ResetAt - now, floored at
// zero so a stale ResetAt in the past never reads as a negative duration.
func TimeToReset(b Bucket, now time.Time) (time.Duration, error) {
	window, length, err := WindowFor(b.Name)
	if err != nil {
		return 0, err
	}
	if window == BucketWindowRolling {
		return time.Duration(float64(length) * b.RemainingFraction), nil
	}
	remaining := b.ResetAt.Sub(now)
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}
