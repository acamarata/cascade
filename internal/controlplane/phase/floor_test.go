package phase

import (
	"testing"
)

// TestPhaseState verifies that the ten PhaseState constants exist and are non-empty.
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
}
