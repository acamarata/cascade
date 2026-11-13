package docket

import "testing"

// TestBypassAdmitsBeyondCapWhenDeadlinesMetAndSeatsSpent verifies that a hot ticket
// is admitted beyond the A3 cap when deadlines are met and no seats are spent.
func TestBypassAdmitsBeyondCapWhenDeadlinesMetAndSeatsSpent(t *testing.T) {
	tests := []struct {
		name      string
		occupancy int
	}{
		{"at cap", 2},
		{"beyond cap", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := BypassInput{
				Lane:          "A3",
				Hot:           true,
				Cap:           2,
				OverflowCap:   0,
				Occupancy:     tt.occupancy,
				GateDeadlines: 3,
				RunSeats:      0,
			}

			decision, err := Bypass(in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !decision.Admitted {
				t.Errorf("expected Admitted=true, got false")
			}
			if !decision.Bypassed {
				t.Errorf("expected Bypassed=true, got false")
			}
		})
	}
}

// TestBypassAdmitsToA2BeyondOverflowCap verifies that a hot ticket is admitted
// beyond the A2 overflow cap independently from the main cap.
func TestBypassAdmitsToA2BeyondOverflowCap(t *testing.T) {
	tests := []struct {
		name      string
		occupancy int
	}{
		{"at overflow cap", 1},
		{"beyond overflow cap", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := BypassInput{
				Lane:          "A2",
				Hot:           true,
				Cap:           10,
				OverflowCap:   1,
				Occupancy:     tt.occupancy,
				GateDeadlines: 4,
				RunSeats:      0,
			}

			decision, err := Bypass(in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !decision.Admitted {
				t.Errorf("expected Admitted=true, got false")
			}
			if !decision.Bypassed {
				t.Errorf("expected Bypassed=true, got false")
			}
		})
	}
}
