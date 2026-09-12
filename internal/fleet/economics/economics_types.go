// Purpose: the R-21.31/R-21.118/R-21.53 shadow-price arithmetic contract --
//   the input and result value types ShadowPrice (shadow_price.go)
//   consumes and returns. Every constant and term this ticket owns is
//   fixed verbatim by 21-T0-RULINGS-R21 R-21.30 (base prices) and R-21.31
//   (the reserve barrier and the SP = B x Q x P_mode x R formula); nothing
//   here is derived, tuned or chosen. The pressure term Q itself is NOT
//   owned by this file: it is P1-E40-W9-S78-T1's
//   topology.BucketPressure/DomainPressure (R-21.53), consumed, never
//   re-implemented, and P_mode arrives from the AO/S-79.T2 mode-multiplier
//   tables as a plain caller-supplied float, never a table this package
//   ships.
//
// Inputs: a lane's identity, its R-21.30 base price, a caller-resolved
//   mode multiplier, the caller-resolved preserve_weekly_reserve and the
//   barrier bucket's remaining fraction (R-21.34), the lane's quota
//   domain (fed to topology.DomainPressure), the domain's current
//   ewma_429 signal (R-21.128 -- see shadow_price.go's CONTRADICTION
//   note for why this is an explicit field the contract's own type
//   listing omits), and the instant now.
//
// Outputs: every one of the four SP terms named separately, plus the two
//   independent reserve/hard-reserve signals AO/S-79.T3 reads for
//   eligibility (BelowHardReserve, overridable by `incident`/
//   `--allow-reserve`; BelowAbsoluteFloor, overridable only by
//   `--allow-reserve`, in every mode).
//
// Constraints: pure value types -- no I/O, no storage, no clock field.
//   `Now` and `Ewma429` arrive as caller-supplied arguments; this package
//   reads no clock and tracks no per-scope 429 history of its own.
//
// SPORT: fleet/economics/pressure/ADD (P1-E41-W9-S79-T1).

package economics

import (
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// ShadowPriceInput is ShadowPrice's argument: everything the R-21.31
// formula needs, already resolved by the caller. This package never
// resolves an AccountID's reserve or a lane's mode itself -- both arrive
// pre-computed (R-21.34 reserve resolution and the AO/S-79.T2 mode
// tables live outside this package).
type ShadowPriceInput struct {
	// LaneID identifies the lane the result is for; carried through
	// unchanged for the caller's own logging/explain output, never
	// consulted by the formula itself.
	LaneID topology.LaneID `json:"lane_id"`
	// BasePrice is the lane's R-21.30 base shadow price (B), typically
	// topology.BaseShadowPrice(lane.LaneClass) or the
	// EffectiveLaneClass-derived executive-overflow price.
	BasePrice float64 `json:"base_price"`
	// ModeMultiplier is P_mode, an AO/S-79.T2 mode-multiplier table
	// lookup this package never performs itself.
	ModeMultiplier float64 `json:"mode_multiplier"`
	// Reserve is the caller-resolved, already-ValidateReserve-clamped
	// preserve_weekly_reserve for the lane's account (R-21.34).
	Reserve float64 `json:"reserve"`
	// RemainingFraction is the barrier bucket's (topology.BarrierBucket)
	// current remaining fraction -- the single scalar ReserveBarrier
	// binds to, distinct from Domain's per-dimension fractions that feed
	// Pressure.
	RemainingFraction float64 `json:"remaining_fraction"`
	// Domain is the lane's quota domain, fed to
	// topology.DomainPressure unchanged.
	Domain topology.QuotaDomain `json:"domain"`
	// Ewma429 is the domain's scope's current ewma_429 signal, R-21.128's
	// unknown-source pressure input. See shadow_price.go's file-level
	// CONTRADICTION note: the contract's literal ShadowPriceInput field
	// list omits this, but topology.DomainPressure's real, already-landed
	// signature requires it explicitly (AN/S-78.T1's own resolution of
	// the identical problem), so this field is added rather than the
	// call site fabricating a value.
	Ewma429 float64 `json:"ewma_429"`
	// Now is the instant the price is computed at.
	Now time.Time `json:"now"`
}

// ShadowPriceResult is ShadowPrice's return value: every SP term named
// separately so a caller (the AO/S-79.T3 explain output, `cascade fleet
// capacity --economics`) never has to recompute one.
type ShadowPriceResult struct {
	// Base is B, ShadowPriceInput.BasePrice unchanged.
	Base float64 `json:"base"`
	// Pressure is Q, topology.DomainPressure's return value unchanged --
	// this package performs no local recomputation of it (R-21.53).
	Pressure float64 `json:"pressure"`
	// ModeMultiplier is P_mode, ShadowPriceInput.ModeMultiplier unchanged.
	ModeMultiplier float64 `json:"mode_multiplier"`
	// ReserveBarrier is R, this package's own ReserveBarrier factor.
	ReserveBarrier float64 `json:"reserve_barrier"`
	// ShadowPrice is SP = Base * Pressure * ModeMultiplier * ReserveBarrier.
	ShadowPrice float64 `json:"shadow_price"`
	// BelowHardReserve is R-21.31's ineligible signal: true at or below
	// the hard reserve. Overridable by the `incident` mode and the
	// `--allow-reserve` verb -- both live in AO/S-79.T3's eligibility
	// filter, never here.
	BelowHardReserve bool `json:"below_hard_reserve"`
	// BelowAbsoluteFloor is R-21.118's THIRD, mode-independent signal:
	// true when RemainingFraction < AbsoluteFloorFactor*Reserve.
	// Independent of BelowHardReserve -- `incident` clears the first and
	// never the second; only the elevated `--allow-reserve` verb can.
	BelowAbsoluteFloor bool `json:"below_absolute_floor"`
}
