package phase

import (
	"testing"
)

// TestPhaseState verifies that PhaseState constants and the Floor stub exist.
func TestPhaseState(t *testing.T) {
	// Verify all ten phase constants are defined.
	phases := []PhaseState{
		Seeded, RTP, Planning, RTF, Forging,
		RTB, Building, RTD, Deploying, Finished,
	}
	for _, p := range phases {
		if p == "" {
			t.Error("phase constant must not be empty")
		}
	}

	// Floor is a stub: it must return an error for any input.
	_, err := Floor(nil, "")
	if err == nil {
		t.Error("Floor stub must return an error for any input")
	}

	_, err = Floor([]string{"T-1"}, "done")
	if err == nil {
		t.Error("Floor stub must return an error for any input")
	}
}
