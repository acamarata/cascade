package topology

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type availabilityGoldenReservation struct {
	ReservationID string `json:"reservation_id"`
	Scope         string `json:"scope"`
	Dimension     string `json:"dimension"`
	State         string `json:"state"`
	Estimate      int64  `json:"estimate"`
}

type availabilityGoldenCase struct {
	Name                      string                          `json:"name"`
	Scope                     string                          `json:"scope"`
	Dimension                 string                          `json:"dimension"`
	CapacityObserved          int64                           `json:"capacity_observed"`
	CommittedSinceObservation int64                           `json:"committed_since_observation"`
	Reservations              []availabilityGoldenReservation `json:"reservations"`
	Want                      int64                           `json:"want"`
}

// TestAvailableDerivedFromReservations is the named acceptance test:
// Available matches testdata/availability.golden.json exactly, proving the
// formula against the spec rather than a second copy of itself.
func TestAvailableDerivedFromReservations(t *testing.T) {
	raw, err := os.ReadFile("testdata/availability.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var cases []availabilityGoldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	now := time.Now()
	for _, c := range cases {
		var rs []ReservationEstimate
		for _, r := range c.Reservations {
			rs = append(rs, ReservationEstimate{
				ReservationID: r.ReservationID,
				LimitScopeID:  LimitScopeID(r.Scope),
				Dimension:     r.Dimension,
				State:         ReservationState(r.State),
				Estimate:      r.Estimate,
			})
		}
		got := Available(LimitScopeID(c.Scope), c.Dimension, c.CapacityObserved, c.CommittedSinceObservation, rs, now)
		if got != c.Want {
			t.Errorf("%s: Available = %d, want %d", c.Name, got, c.Want)
		}
	}
}

func TestRemainingFractionDerivedWhenObserved(t *testing.T) {
	b := Bucket{CapacityObserved: 100, CommittedSinceObservation: 40, RemainingFraction: 0.99}
	got := RemainingFraction(b, time.Now())
	if got != 0.6 {
		t.Errorf("RemainingFraction = %v, want 0.6 (derived, ignoring the stale stored field)", got)
	}
}

func TestRemainingFractionFallsBackBeforeObservation(t *testing.T) {
	b := Bucket{CapacityObserved: UnobservedCapacity, RemainingFraction: 0.42}
	got := RemainingFraction(b, time.Now())
	if got != 0.42 {
		t.Errorf("RemainingFraction (unobserved) = %v, want the stored field 0.42", got)
	}
}

func TestRemainingFractionClampedToUnitRange(t *testing.T) {
	over := RemainingFraction(Bucket{CapacityObserved: 10, CommittedSinceObservation: -5}, time.Now())
	if over != 1 {
		t.Errorf("RemainingFraction over-full = %v, want 1", over)
	}
	under := RemainingFraction(Bucket{CapacityObserved: 10, CommittedSinceObservation: 100}, time.Now())
	if under != 0 {
		t.Errorf("RemainingFraction over-committed = %v, want 0", under)
	}
}
