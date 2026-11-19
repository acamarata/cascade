package consult

import (
	"testing"
)

func TestAllowedCoordination(t *testing.T) {
	var log []NativeTokenViolation
	out, err := ValidateNativeTokenUse("T0 coordination", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(log) {
		t.Fatalf("expected identical log, got %v", out)
	}

	outNil, errNil := ValidateNativeTokenUse("T0 coordination", nil)
	if errNil != nil {
		t.Fatalf("unexpected error on nil log: %v", errNil)
	}
	if outNil != nil {
		t.Fatalf("expected nil log to remain nil, got %v", outNil)
	}
}

func TestAllowedCRQA(t *testing.T) {
	log := []NativeTokenViolation{{Code: "PRE", Use: "existing"}}
	out, err := ValidateNativeTokenUse("CR/QA", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(log) {
		t.Fatalf("expected identical log, got %v", out)
	}
	if out[0].Code != log[0].Code || out[0].Use != log[0].Use {
		t.Fatalf("expected original log unchanged, got %v", out)
	}
}

func TestAllowedEscalation(t *testing.T) {
	log := []NativeTokenViolation{{Code: "PRE", Use: "existing"}}
	out, err := ValidateNativeTokenUse("hard-ticket escalation", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(log) {
		t.Fatalf("expected identical log, got %v", out)
	}
	if out[0].Code != log[0].Code || out[0].Use != log[0].Use {
		t.Fatalf("expected original log unchanged, got %v", out)
	}
}

func TestAllowedPlanningSignOff(t *testing.T) {
	log := []NativeTokenViolation{{Code: "PRE", Use: "existing"}}
	out, err := ValidateNativeTokenUse("planning sign-off", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(log) {
		t.Fatalf("expected identical log, got %v", out)
	}
	if out[0].Code != log[0].Code || out[0].Use != log[0].Use {
		t.Fatalf("expected original log unchanged, got %v", out)
	}
}
