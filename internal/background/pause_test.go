package background

import "testing"

func TestPauseOnExitAutonomousActive(t *testing.T) {
	if got := PauseOnExit("Autonomous", true); got {
		t.Fatalf("PauseOnExit(%q, true) = %v, want false", "Autonomous", got)
	}
}

func TestPauseOnExitAutonomousInactive(t *testing.T) {
	if got := PauseOnExit("Autonomous", false); !got {
		t.Fatalf("PauseOnExit(%q, false) = %v, want true", "Autonomous", got)
	}
}

func TestPauseOnExitManualMode(t *testing.T) {
	if got := PauseOnExit("Manual", false); got {
		t.Fatalf("PauseOnExit(%q, false) = %v, want false", "Manual", got)
	}
}

func TestPauseOnExitEmptyMode(t *testing.T) {
	if got := PauseOnExit("", false); got {
		t.Fatalf("PauseOnExit(%q, false) = %v, want false", "", got)
	}
}
