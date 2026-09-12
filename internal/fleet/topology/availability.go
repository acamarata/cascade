// Purpose: R-21.114's reservation-first availability. quota_bucket (the
//
//	sessions-domain table quota_store.go declares) is a READ-ONLY
//	observation cache -- no dispatch path writes it. Available is a PURE
//	function of the bucket's absolute counters and the reservation
//	estimates passed in; it never touches the store, which is what makes
//	"no dispatch path mutates quota_bucket" true by construction rather
//	than by convention.
//
// Inputs: a scope, a dimension, the absolute counters and a slice of
//
//	ReservationEstimate. Outputs: the available capacity.
//
// Constraints: this package ships no counter decrement and no rollback
//
//	path -- the reservation ROW itself lives in the jobs domain
//	(AO/S-79.T4), inserted first, which is what makes a crash mid-flight
//	crash-safe without an un-decrement step here.
//
// SPORT: fleet/topology/availability/ADD (P1-E40-W9-S77-T3).

package topology

import "time"

// ReservationState is the closed held|committed vocabulary a
// ReservationEstimate carries.
type ReservationState string

// The two closed ReservationState members.
const (
	ReservationHeld      ReservationState = "held"
	ReservationCommitted ReservationState = "committed"
)

// ReservationEstimate is the small value type Available sums over. The
// reservation row itself (id, timestamps, terminal state) lives in the
// jobs domain (AO/S-79.T4); this package only ever sees this projection.
type ReservationEstimate struct {
	ReservationID string
	LimitScopeID  LimitScopeID
	Dimension     string
	State         ReservationState
	Estimate      int64
}

// Available returns capacityObserved - committedSinceObservation - the
// sum of every held or committed reservation's Estimate on (scope,
// dimension) in rs, per R-21.114. now is accepted for a future expiry-
// aware accounting pass; this ticket's formula does not time-filter rs
// (a reservation's own terminal-state transition, not elapsed time,
// retires it). Available never reads or writes any store -- it is a pure
// function of its arguments, which is the whole of the "no dispatch path
// mutates quota_bucket" guarantee.
func Available(scope LimitScopeID, dimension string, capacityObserved, committedSinceObservation int64, rs []ReservationEstimate, now time.Time) int64 {
	_ = now
	var committed int64
	for _, r := range rs {
		if r.LimitScopeID != scope || r.Dimension != dimension {
			continue
		}
		if r.State != ReservationHeld && r.State != ReservationCommitted {
			continue
		}
		committed += r.Estimate
	}
	return capacityObserved - committedSinceObservation - committed
}

// RemainingFraction derives Bucket.RemainingFraction's authoritative value
// at instant now: when b.CapacityObserved has been reconciled (>= 0), the
// fraction is derived from the absolute counters
// (1 - committed/capacity, floored at 0) rather than trusting the stored
// field, per R-21.96 ("never an independently written number"). Before any
// absolute observation exists (CapacityObserved == UnobservedCapacity),
// the stored field is the only signal available and is returned unchanged.
func RemainingFraction(b Bucket, now time.Time) float64 {
	_ = now
	if b.CapacityObserved == UnobservedCapacity || b.CapacityObserved == 0 {
		return b.RemainingFraction
	}
	frac := 1 - float64(b.CommittedSinceObservation)/float64(b.CapacityObserved)
	if frac < 0 {
		return 0
	}
	if frac > 1 {
		return 1
	}
	return frac
}
