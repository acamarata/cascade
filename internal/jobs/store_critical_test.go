package jobs

import (
	"context"
	"testing"
)

// criticalInFlightFor stores one job with the given risk class and state and
// returns what CriticalInFlight reports.
func criticalInFlightFor(t *testing.T, id, risk string, st JobState) bool {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob(id)
	j.RiskClass = risk
	j.State = st
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	ok, err := s.CriticalInFlight(ctx)
	if err != nil {
		t.Fatalf("CriticalInFlight: %v", err)
	}
	return ok
}

func TestCriticalInFlight(t *testing.T) {
	inFlightStates := []JobState{
		JobStateLeased,
		JobStateRunning,
		JobStateVerifying,
		JobStateReviewing,
	}
	for _, st := range inFlightStates {
		t.Run(string(st), func(t *testing.T) {
			if !criticalInFlightFor(t, "job-"+string(st), "critical", st) {
				t.Errorf("CriticalInFlight = false, want true for state %s", st)
			}
		})
	}

	t.Run("pending", func(t *testing.T) {
		if criticalInFlightFor(t, "job-pending", "critical", JobStatePending) {
			t.Error("CriticalInFlight = true, want false for pending critical job")
		}
	})

	t.Run("accepted", func(t *testing.T) {
		if criticalInFlightFor(t, "job-accepted", "critical", JobStateAccepted) {
			t.Error("CriticalInFlight = true, want false for accepted critical job")
		}
	})

	t.Run("high_running", func(t *testing.T) {
		if criticalInFlightFor(t, "job-high-running", "high", JobStateRunning) {
			t.Error("CriticalInFlight = true, want false for high running job")
		}
	})

	t.Run("closed_store", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.db.Close(); err != nil {
			t.Fatalf("db.Close: %v", err)
		}
		_, err := s.CriticalInFlight(context.Background())
		if err == nil {
			t.Error("CriticalInFlight on closed store = nil, want error")
		}
	})
}
