// Purpose: R-21.31's reserve barrier and four-term shadow price, and
//   R-21.118's mode-independent absolute floor. Together with
//   economics_types.go this is the arithmetic floor of the scarcity
//   scheduler (AO/S-79.T3 ranks lanes with these numbers, AO/S-79.T4
//   reserves against them, AO/S-80.T3 prints them); every constant here
//   is fixed verbatim, none is derived or tuned.
//
// Inputs: a remaining fraction and a reserve (ReserveBarrier), or a full
//   ShadowPriceInput (ShadowPrice).
// Outputs: the reserve-barrier factor plus its two independent boolean
//   signals, or the full ShadowPriceResult.
// Constraints: pure functions -- no store, no clock field, no package
//   state, no second BucketPressure/DomainPressure implementation
//   (R-21.53: this file consumes topology.DomainPressure, never
//   recomputes it).
//
// CONTRADICTION (contract vs. tree, resolved here -- see the journal for
// the full quote): the contract's task list writes the call as
// `topology.DomainPressure(in.Domain, in.Reserve, in.Now)`, a three-
// argument call, and ShadowPriceInput's own listed fields carry no
// ewma_429. But topology.DomainPressure's real, already-landed signature
// (chooser_pressure.go, AN/S-78.T1) is
// `DomainPressure(d QuotaDomain, reserve float64, ewma429 float64, now
// time.Time) float64` -- four arguments, ewma429 required between reserve
// and now, per that file's own identical CONTRADICTION note explaining
// why a pure function cannot read a scope's 429 history from a Bucket or
// QuotaDomain alone. Resolution: ShadowPriceInput carries an explicit
// Ewma429 field (economics_types.go) and this file passes it through,
// mirroring AN/S-78.T1's own precedent rather than inventing a second
// one.
//
// SPORT: fleet/economics/pressure/ADD (P1-E41-W9-S79-T1).

package economics

import "github.com/acamarata/cascade/internal/fleet/topology"

// ReserveBarrierBand is the R-21.31 soft-band width above reserve where
// the barrier factor interpolates linearly.
const ReserveBarrierBand = 0.10

// ReserveBarrierMax is the barrier factor at or below the hard reserve.
const ReserveBarrierMax = 4.0

// AbsoluteFloorFactor is R-21.118's mode-independent floor: a lane whose
// barrier-bucket remaining fraction falls below AbsoluteFloorFactor times
// its reserve is ineligible in every mode, including `incident`.
const AbsoluteFloorFactor = 0.5

// reserveBarrierMin is the factor above reserve+ReserveBarrierBand.
const reserveBarrierMin = 1.0

// ReserveBarrier returns the R-21.31 reserve-barrier factor for
// remainingFraction against reserve: 1.0 while remainingFraction is
// strictly above reserve+ReserveBarrierBand; linear from 1.0 to
// ReserveBarrierMax across [reserve, reserve+ReserveBarrierBand]; exactly
// ReserveBarrierMax with atOrBelowReserve true at or below reserve.
// belowAbsoluteFloor is R-21.118's independent third signal -- true when
// remainingFraction < AbsoluteFloorFactor*reserve, computed unconditionally
// and never folded into atOrBelowReserve or the returned factor (the
// factor stays at ReserveBarrierMax below the floor; it is not a fourth
// multiplier band).
func ReserveBarrier(remainingFraction, reserve float64) (factor float64, atOrBelowReserve, belowAbsoluteFloor bool) {
	belowAbsoluteFloor = remainingFraction < AbsoluteFloorFactor*reserve
	upper := reserve + ReserveBarrierBand
	switch {
	case remainingFraction <= reserve:
		return ReserveBarrierMax, true, belowAbsoluteFloor
	case remainingFraction > upper:
		return reserveBarrierMin, false, belowAbsoluteFloor
	default:
		span := upper - remainingFraction
		frac := span / ReserveBarrierBand
		return reserveBarrierMin + frac*(ReserveBarrierMax-reserveBarrierMin), false, belowAbsoluteFloor
	}
}

// ShadowPrice computes R-21.31's four-term shadow price
// SP = B * Q * P_mode * R for in, where Q is
// topology.DomainPressure(in.Domain, in.Reserve, in.Ewma429, in.Now)
// (AN/S-78.T1, R-21.53 -- no second pressure implementation here) and R
// is ReserveBarrier(in.RemainingFraction, in.Reserve). Every term is
// returned separately in ShadowPriceResult so a caller never recomputes
// one.
func ShadowPrice(in ShadowPriceInput) ShadowPriceResult {
	pressure := topology.DomainPressure(in.Domain, in.Reserve, in.Ewma429, in.Now)
	barrier, atOrBelowReserve, belowFloor := ReserveBarrier(in.RemainingFraction, in.Reserve)
	price := in.BasePrice * pressure * in.ModeMultiplier * barrier
	return ShadowPriceResult{
		Base:               in.BasePrice,
		Pressure:           pressure,
		ModeMultiplier:     in.ModeMultiplier,
		ReserveBarrier:     barrier,
		ShadowPrice:        price,
		BelowHardReserve:   atOrBelowReserve,
		BelowAbsoluteFloor: belowFloor,
	}
}
