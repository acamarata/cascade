package lifecycle

import (
	"errors"
	"testing"
)

// TestPhaseConstantsExist verifies that the Phase type has ten constants
// matching Conductor Lifecycle names and that ErrIllegalTransition is non-nil.
func TestPhaseConstantsExist(t *testing.T) {
	phases := []Phase{
		Seeded,
		RTP,
		RTB,
		Planning,
		RTF,
		Forging,
		Building,
		RTD,
		Deploying,
		Finished,
	}

	if len(phases) != 10 {
		t.Fatalf("expected 10 phase constants, got %d", len(phases))
	}

	seen := make(map[Phase]bool)
	for _, p := range phases {
		if p == "" {
			t.Fatal("phase constant must not be empty")
		}
		if seen[p] {
			t.Fatalf("duplicate phase constant: %s", p)
		}
		seen[p] = true
	}

	if ErrIllegalTransition == nil {
		t.Fatal("ErrIllegalTransition must not be nil")
	}

	var err error = ErrIllegalTransition
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatal("ErrIllegalTransition does not match itself via errors.Is")
	}
}
