package docket

import "testing"

func TestValidateNoRunningState_AllowsSafeOp(t *testing.T) {
	err := ValidateNoRunningState("safe-op")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateNoRunningState_DetectsWrite(t *testing.T) {
	err := ValidateNoRunningState("write-running-state")
	if err != ErrD1135 {
		t.Fatalf("expected ErrD1135, got %v", err)
	}
}

func TestValidateNoRunningState_DetectsRead(t *testing.T) {
	err := ValidateNoRunningState("read-running-state")
	if err != ErrD1135 {
		t.Fatalf("expected ErrD1135, got %v", err)
	}
}

func TestValidateNoRunningState_RejectsEmptyPath(t *testing.T) {
	err := ValidateNoRunningState("")
	if err != ErrD1135 {
		t.Fatalf("expected ErrD1135, got %v", err)
	}
}
