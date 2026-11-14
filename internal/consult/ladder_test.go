package consult

import "testing"

// TestSureNoRefusesImmediately verifies immediate refusal per M1894.
func TestSureNoRefusesImmediately(t *testing.T) {
	rs := []Response{
		{Role: "manager", Answer: "yes", Gateway: false},
		{Role: "employee", Answer: "sure no", Gateway: false},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "refuse" {
		t.Errorf("expected outcome refuse, got %s", res.Outcome)
	}
}

// TestGatewayCEOYesCarriesAlone verifies that a genuine gateway CEO yes carries alone.
func TestGatewayCEOYesCarriesAlone(t *testing.T) {
	rs := []Response{
		{Role: "ceo", Answer: "yes", Gateway: true},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "approve" {
		t.Errorf("expected outcome approve, got %s", res.Outcome)
	}
	if !res.CarriedAlone {
		t.Errorf("expected CarriedAlone to be true for gateway CEO yes")
	}
}

// TestNonGatewayCEOYesDoesNotCarryAlone verifies that a non-gateway CEO yes does not carry alone.
func TestNonGatewayCEOYesDoesNotCarryAlone(t *testing.T) {
	rs := []Response{
		{Role: "ceo", Answer: "yes", Gateway: false},
	}
	res := EvaluateLadder(rs)
	if res.CarriedAlone {
		t.Errorf("expected CarriedAlone to be false for non-gateway CEO yes")
	}
}

// TestMixedResponsesProceed verifies that mixed non-sure-no responses proceed.
func TestMixedResponsesProceed(t *testing.T) {
	rs := []Response{
		{Role: "manager", Answer: "yes", Gateway: false},
		{Role: "employee", Answer: "maybe", Gateway: false},
	}
	res := EvaluateLadder(rs)
	if res.Outcome != "proceed" {
		t.Errorf("expected outcome proceed, got %s", res.Outcome)
	}
}
