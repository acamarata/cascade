// Purpose: R-21.117's reserve vocabulary and bounds -- three quantities
//
//	that stop sharing a name. executive_model_fraction_soft/hard name
//	R-21.34's executive-role eligibility caps (never the barrier).
//	ValidateReserve clamps preserve_weekly_reserve into [0.0, 0.60].
//	BarrierBucket names the one bucket per domain kind the reserve
//	barrier binds to.
//
// Inputs: a raw preserve_weekly_reserve value, or a QuotaDomainKind.
// Outputs: a safe reserve value plus an optional ErrConfigRange, or a
//
//	barrier bucket dimension name.
//
// Constraints: ValidateReserve ALWAYS returns a value safe to use in
//
//	[0.0, 0.60], even when it also reports ErrConfigRange, so a caller
//	that only checks the returned value (never the error) can still never
//	make a lane permanently ineligible.
//
// SPORT: fleet/topology/reserve_bounds/ADD (P1-E40-W9-S77-T3).

package topology

import "github.com/acamarata/cascade/pkg/cascade"

// ExecutiveModelFractionSoft and ExecutiveModelFractionHard are R-21.34's
// executive-role eligibility caps' canonical names, used everywhere in
// this package's surface -- they gate executive-ROLE eligibility only and
// are never a BarrierBucket return value.
const (
	ExecutiveModelFractionSoft = "executive_model_fraction_soft"
	ExecutiveModelFractionHard = "executive_model_fraction_hard"
)

// reserveMin and reserveMax are preserve_weekly_reserve's closed bounds.
const (
	reserveMin = 0.0
	reserveMax = 0.60
)

// ValidateReserve clamps v into [0.0, 0.60] and always returns a value
// safe to use in that range. A v outside the range additionally returns
// the typed ErrConfigRange, naming the offending value, so a caller that
// checks the error can log/alert while the returned, already-clamped
// value keeps the lane eligible either way (a raw reserve of 1.0 can
// never permanently disable a lane).
func ValidateReserve(v float64) (float64, error) {
	if v >= reserveMin && v <= reserveMax {
		return v, nil
	}
	clamped := v
	if clamped < reserveMin {
		clamped = reserveMin
	}
	if clamped > reserveMax {
		clamped = reserveMax
	}
	return clamped, cascade.Wrapf(cascade.KindInvalidInput, ErrConfigRange, "preserve_weekly_reserve %v outside [%.2f,%.2f]", v, reserveMin, reserveMax)
}

// BarrierBucket returns the single named bucket the reserve barrier binds
// to for kind: weekly_shared for subscription_window, weekly for
// shared_pool, rpd for api_project. Any other kind is ErrTopologyInvariant
// -- there is no default barrier bucket.
func BarrierBucket(kind QuotaDomainKind) (string, error) {
	switch kind {
	case QuotaDomainSubscriptionWindow:
		return DimensionWeeklyShared, nil
	case QuotaDomainSharedPool:
		return DimensionWeekly, nil
	case QuotaDomainAPIProject:
		return DimensionRPD, nil
	default:
		return "", newInvariantErr("barrier_bucket", string(kind), "no barrier bucket is defined for this domain kind")
	}
}
