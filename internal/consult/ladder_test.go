package consult

import "testing"

// TestSureNoRefusesImmediately verifies immediate refusal per M1894.
func TestSureNoRefusesImmediately(t *testing.T) {
	rs := []Response{
		{Role: "manager", Answer: "yes", GC: false},
		{Role: "employee", Answer: "sure no", GC: false},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "refuse" {
		t.Errorf("expected outcome refuse, got %s", res.Outcome)
	}
}

// TestGCCEOYesCarriesAlone verifies that a genuine GC CEO yes carries alone.
func TestGCCEOYesCarriesAlone(t *testing.T) {
	rs := []Response{
		{Role: "ceo", Answer: "yes", GC: true},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "approve" {
		t.Errorf("expected outcome approve, got %s", res.Outcome)
	}
	if !res.CarriedAlone {
		t.Errorf("expected CarriedAlone to be true for GC CEO yes")
	}
}

// TestNonGCCEOYesDoesNotCarryAlone verifies that a non-GC CEO yes does not carry alone.
func TestNonGCCEOYesDoesNotCarryAlone(t *testing.T) {
	rs := []Response{
		{Role: "ceo", Answer: "yes", GC: false},
	}
	res := EvaluateLadder(rs)
	if res.CarriedAlone {
		t.Errorf("expected CarriedAlone to be false for non-GC CEO yes")
	}
}

// TestMixedResponsesProceed verifies that mixed non-sure-no responses proceed.
func TestMixedResponsesProceed(t *testing.T) {
	rs := []Response{
		{Role: "manager", Answer: "yes", GC: false},
		{Role: "employee", Answer: "maybe", GC: false},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "proceed" {
		t.Errorf("expected outcome proceed, got %s", res.Outcome)
	}
}
