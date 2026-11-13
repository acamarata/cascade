package background

import "testing"

func TestGuardSilentWhenSupervisorRunning(t *testing.T) {
	got := GuardState(true)
	if got != autonomousSilent {
		t.Fatalf("GuardState(true) = %q, want %q", got, autonomousSilent)
	}
}

func TestGuardNotSilentWhenSupervisorStopped(t *testing.T) {
	got := GuardState(false)
	if got == autonomousSilent {
		t.Fatalf("GuardState(false) = %q, want a state other than %q", got, autonomousSilent)
	}
	if got != guardNotSilent {
		t.Fatalf("GuardState(false) = %q, want %q", got, guardNotSilent)
	}
}

func TestGuardSilenceEqualsAutonomousSilent(t *testing.T) {
	if GuardSilence != autonomousSilent {
		t.Fatalf("GuardSilence = %q, want %q", GuardSilence, autonomousSilent)
	}
}
