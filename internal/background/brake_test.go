package background

import (
	"errors"
	"testing"
)

// TestBudgetExhaustionTriggersBrake verifies that budget exhaustion sets the brake to braked state.
func TestBudgetExhaustionTriggersBrake(t *testing.T) {
	b := &Brake{State: StateRunning}
	b.Check(0)
	if b.State != StateBraked {
		t.Errorf("expected state %s, got %s", StateBraked, b.State)
	}
}

// TestOwnerResumeClearsBrake verifies that owner resume action clears the brake.
func TestOwnerResumeClearsBrake(t *testing.T) {
	b := &Brake{State: StateBraked}
	err := b.Resume("resume")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if b.State != StateRunning {
		t.Errorf("expected state %s, got %s", StateRunning, b.State)
	}
}

// TestOperationsHaltedDuringBrake verifies that operations return halted error when braked.
func TestOperationsHaltedDuringBrake(t *testing.T) {
	b := &Brake{State: StateBraked}
	err := b.Do()
	if !errors.Is(err, ErrHalted) {
		t.Errorf("expected error %v, got %v", ErrHalted, err)
	}
}

// TestResumeWithoutOwnerActionFails verifies that invalid owner action returns unauthorized error.
func TestResumeWithoutOwnerActionFails(t *testing.T) {
	b := &Brake{State: StateBraked}
	err := b.Resume("none")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected error %v, got %v", ErrUnauthorized, err)
	}
}
