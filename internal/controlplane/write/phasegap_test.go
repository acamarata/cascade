package write

import "testing"

func TestPhaseGapP11Valid(t *testing.T) {
	valid, err := ValidatePhaseGap("p11", 5, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatalf("expected p11 gap 5 to be valid")
	}
}

func TestPhaseGapP12Valid(t *testing.T) {
	valid, err := ValidatePhaseGap("p12", 3, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatalf("expected p12 gap 3 to be valid")
	}
}

func TestPhaseGapP11PostMerge(t *testing.T) {
	valid, err := ValidatePhaseGap("p11", 5, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatalf("expected merged p11 gap 5 to be valid")
	}
}

func TestPhaseGapP12PostMerge(t *testing.T) {
	valid, err := ValidatePhaseGap("p12", 3, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatalf("expected merged p12 gap 3 to be valid")
	}
}

func TestPhaseGapInvalidSupervisor(t *testing.T) {
	valid, err := ValidatePhaseGap("p99", 5, false)
	if valid {
		t.Fatalf("expected unknown supervisor to be invalid")
	}
	if err == nil {
		t.Fatalf("expected error for unknown supervisor")
	}

	pgErr, ok := err.(PhaseGapError)
	if !ok {
		t.Fatalf("expected PhaseGapError, got %T", err)
	}
	if pgErr.Code != "T0588" {
		t.Fatalf("expected error code T0588, got %s", pgErr.Code)
	}
}
