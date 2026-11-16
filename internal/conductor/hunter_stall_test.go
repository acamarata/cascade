package conductor

import "testing"

// TestTypesExist verifies that the stall detection types and constants are defined.
func TestTypesExist(t *testing.T) {
	// Verify RunState struct exists and has expected zero-value behavior
	var state RunState
	if state.RefusedCount != 0 {
		t.Errorf("expected RefusedCount == 0, got %d", state.RefusedCount)
	}

	// Verify StallClass constants exist
	_ = StallClassStalled
	_ = StallClassRefused
	_ = StallClassBlocked
	_ = StallClassRedCI

	// Verify HunterAction constants exist
	_ = ActionRelaunch
	_ = ActionRequeue
	_ = ActionMessage
	_ = ActionRaiseWidth

	// Verify StallDetector interface exists by declaring a variable of that type
	var _ StallDetector
}
