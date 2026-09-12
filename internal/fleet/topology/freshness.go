// Purpose: R-21.109's per-source observation freshness, Select (the
//
//	highest-precedence NON-EXPIRED observation), the sliding-window
//	rolling-consumption sum, and the all-expired fallback that keeps a
//	bucket's caps while marking it unknown.
//
// Inputs: a set of Bucket observations of the same dimension, plus the
//
//	current instant. Outputs: the winning Bucket.
//
// Constraints: a stale provider-status observation must not outrank a
//
//	live cli-observation; SourceUnknown never expires; the S-77.T3 ladder
//	precedence itself (source_ladder.go's pickBetter) is unchanged --
//	Select only ADDS an expiry filter in front of it.
//
// SPORT: fleet/topology/freshness/ADD (P1-E40-W9-S77-T3).

package topology

import "time"

// sourceExpiry is R-21.109's per-source expiry table. A zero value here
// (never used as a real entry) would mean "expires immediately"; absence
// from the map means "never expires" (SourceUnknown), checked explicitly
// by Expired rather than by a sentinel duration.
var sourceExpiry = map[BucketSource]time.Duration{
	SourceProviderStatus: 10 * time.Minute,
	SourceCLIObservation: 30 * time.Minute,
	SourceUserEstimate:   24 * time.Hour,
}

// Expired reports whether b's observation is stale at instant now, per its
// Source's expiry. SourceUnknown never expires.
func Expired(b Bucket, now time.Time) bool {
	if b.Source == SourceUnknown {
		return false
	}
	ttl, ok := sourceExpiry[b.Source]
	if !ok {
		return false
	}
	return now.Sub(b.ObservedAt) > ttl
}

// Select returns the highest-precedence NON-EXPIRED observation among
// observations, using source_ladder.go's pickBetter tie-break unchanged.
// When every observation has expired, the highest-precedence observation
// (by pickBetter, ignoring expiry) is returned with its concurrency and
// request caps (Limit, LimitScopeID) intact but its Source downgraded to
// SourceUnknown and Confidence reset to 0, so its reserve status reports
// as unknown rather than as the stale full-capacity reading. An empty
// observations slice is ErrTopologyInvariant -- there is nothing to
// select from.
func Select(observations []Bucket, now time.Time) (Bucket, error) {
	if len(observations) == 0 {
		return Bucket{}, newInvariantErr("freshness", "", "Select requires at least one observation")
	}

	var bestLive Bucket
	haveLive := false
	var bestAny Bucket
	haveAny := false

	for _, o := range observations {
		if !haveAny {
			bestAny = o
			haveAny = true
		} else {
			bestAny = pickBetter(bestAny, o)
		}
		if Expired(o, now) {
			continue
		}
		if !haveLive {
			bestLive = o
			haveLive = true
		} else {
			bestLive = pickBetter(bestLive, o)
		}
	}

	if haveLive {
		return bestLive, nil
	}

	fallback := bestAny
	fallback.Source = SourceUnknown
	fallback.Confidence = 0
	return fallback, nil
}

// ConsumptionEvent is one timestamped consumption sample for a rolling
// (rpm/tpm) dimension -- R-21.109's "TIMESTAMPED CONSUMPTION" rather than
// linear fixed-window decay.
type ConsumptionEvent struct {
	At     time.Time
	Amount int64
}

// SlidingWindowSum sums every event whose At falls within
// (now-windowLength, now], the sliding-window consumption a rolling
// dimension's pressure reads instead of a linear-decay approximation.
func SlidingWindowSum(events []ConsumptionEvent, now time.Time, windowLength time.Duration) int64 {
	cutoff := now.Add(-windowLength)
	var sum int64
	for _, e := range events {
		if e.At.After(cutoff) && !e.At.After(now) {
			sum += e.Amount
		}
	}
	return sum
}
