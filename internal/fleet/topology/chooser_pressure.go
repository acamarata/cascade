// Purpose: R-21.31/R-21.53's pressure scalar -- the ONE home of the
//
//	formula, computed once here so internal/fleet/economics (AO/S-79.T1)
//	builds shadow price, mode multiplier and the reserve barrier ON TOP
//	of it rather than writing a second implementation (forced by the
//	dependency direction: economics imports topology, never the reverse).
//
// Inputs: a Bucket or QuotaDomain, a caller-supplied reserve in [0,1), the
//
//	scope's current ewma_429 and the instant now.
//
// Constraints: pure functions -- no store, no clock field, no package
//
//	state. windowLength/timeToReset always come from window_map.go's
//	WindowFor, so no dimension can yield a zero or absent window. An
//	unknown-source bucket is priced from its 429 history (R-21.128), never
//	the flat 1.0 the R-21.31 formula would otherwise produce, so it is
//	never treated as permanently on-budget.
//
// CONTRADICTION (contract vs. formula, resolved here -- see journal):
// the contract's task list states the literal signatures
// `BucketPressure(b Bucket, reserve float64, now time.Time) float64` and
// `DomainPressure(d QuotaDomain, reserve float64, now time.Time) float64`
// with no ewma_429 parameter, but the R-21.128 formula these same
// functions must implement is `clamp(1.0 + 3.0*ewma_429(scope), 1.0, 25)`
// for an unknown-source bucket -- a value neither function can read from a
// Bucket or QuotaDomain alone without either (a) hidden package state,
// which breaks Art.7 determinism and the "pure function" contract these
// two functions otherwise satisfy, or (b) a package-level mutable table
// keyed by scope, which fails concurrently and cannot be golden-tested
// deterministically. Resolution: both functions take an explicit
// `ewma429 float64` parameter (the caller's already-computed
// ewma_429(scope), from chooser_score.go's Signals or a test fixture)
// ahead of `now`. This keeps both functions pure and matches the six
// pinned goldens, two of which vary only in this value ("no 429 history"
// vs "sustained 429").
//
// SPORT: fleet/topology/chooser_pressure/ADD (P1-E40-W9-S78-T1).

package topology

import "time"

// pressureEps is R-21.31's eps constant, avoiding a division by zero when
// actualRemaining is exactly zero.
const pressureEps = 0.01

// pressureMin and pressureMax are R-21.31's clamp bounds.
const (
	pressureMin = 0.5
	pressureMax = 25.0
)

// unknownPressureFloor is R-21.128's floor for an unknown-source bucket:
// even with zero 429 history it prices at least as expensive as the
// R-21.31 floor's upper half, never as cheap as a healthy known bucket.
const unknownPressureFloor = 1.0

// unknownPressureSlope is R-21.128's ewma_429 multiplier.
const unknownPressureSlope = 3.0

// clampPressure bounds v to [lo, hi].
func clampPressure(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// targetRemaining computes R-21.31's target-remaining-fraction curve at
// timeToReset within a window of windowLength, for a caller-supplied
// reserve in [0,1).
func targetRemaining(reserve float64, timeToReset, windowLength time.Duration) float64 {
	if windowLength <= 0 {
		return reserve
	}
	frac := float64(timeToReset) / float64(windowLength)
	return reserve + (1-reserve)*frac
}

// BucketPressure returns b's R-21.31 pressure scalar at instant now, given
// a caller-supplied reserve in [0,1) and the owning scope's current
// ewma_429 (see the file-level CONTRADICTION note for why this parameter
// exists). An unknown-source bucket is priced by the R-21.128 formula
// instead of the reserve/window curve, so it is never read as permanently
// on-budget; every other source uses the R-21.31 curve against
// window_map.go's mandatory (window, windowLength) pair, which can never
// be zero or absent.
func BucketPressure(b Bucket, reserve float64, ewma429 float64, now time.Time) float64 {
	if b.Source == SourceUnknown {
		return clampPressure(unknownPressureFloor+unknownPressureSlope*ewma429, unknownPressureFloor, pressureMax)
	}
	_, windowLength, err := WindowFor(b.Name)
	if err != nil {
		// A dimension outside the mandatory map (or a gauge) cannot be
		// priced -- fail closed to the maximum rather than guess.
		return pressureMax
	}
	ttr, err := TimeToReset(b, now)
	if err != nil {
		return pressureMax
	}
	target := targetRemaining(reserve, ttr, windowLength)
	actual := RemainingFraction(b, now)
	ratio := (target + pressureEps) / (actual + pressureEps)
	return clampPressure(ratio*ratio, pressureMin, pressureMax)
}

// DomainPressure returns the MAX BucketPressure over every real dimension
// in d.Dimensions (never d.Batch's gauges, which are not in that map and
// gate eligibility only -- dimensions.go). A domain with no dimensions
// reports pressureMin: an empty domain is never treated as under
// pressure, but Candidates' eligibility predicate refuses it on other
// grounds before pressure is ever consulted.
func DomainPressure(d QuotaDomain, reserve float64, ewma429 float64, now time.Time) float64 {
	highest := pressureMin
	for _, b := range d.Dimensions {
		p := BucketPressure(b, reserve, ewma429, now)
		if p > highest {
			highest = p
		}
	}
	return highest
}
