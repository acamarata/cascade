package topology

import (
	"testing"
	"time"
)

// TestAvailableCountsParked: a parked reservation still holds its
// estimate, so Available subtracts it like held and committed ones on
// the same scope and dimension, and ignores other scopes, other
// dimensions and states outside the three active ones.
func TestAvailableCountsParked(t *testing.T) {
	scope := LimitScopeID("scope:acct-1")
	rs := []ReservationEstimate{
		{ReservationID: "h", LimitScopeID: scope, Dimension: DimensionRPM, State: ReservationHeld, Estimate: 3},
		{ReservationID: "p", LimitScopeID: scope, Dimension: DimensionRPM, State: ReservationParked, Estimate: 5},
		{ReservationID: "c", LimitScopeID: scope, Dimension: DimensionRPM, State: ReservationCommitted, Estimate: 7},
		{ReservationID: "x", LimitScopeID: "scope:other", Dimension: DimensionRPM, State: ReservationParked, Estimate: 100},
		{ReservationID: "y", LimitScopeID: scope, Dimension: DimensionTPM, State: ReservationParked, Estimate: 100},
		{ReservationID: "z", LimitScopeID: scope, Dimension: DimensionRPM, State: ReservationState("released"), Estimate: 100},
	}
	if got := Available(scope, DimensionRPM, 100, 10, rs, time.Time{}); got != 100-10-3-5-7 {
		t.Fatalf("Available = %d, want %d (parked counted like held and committed)", got, 100-10-3-5-7)
	}
	if ReservationParked != "parked" {
		t.Fatalf("ReservationParked = %q, want parked", ReservationParked)
	}
}
