package hook

import "testing"

// TestGateTypesExist verifies that the gate types and constants are properly defined.
func TestGateTypesExist(t *testing.T) {
	// Assert that GateError.Error() returns only the rule name
	err := GateError{RuleName: "test"}
	if err.Error() != "test" {
		t.Errorf("expected GateError.Error() to return \"test\", got %q", err.Error())
	}

	// Assert that SessionContext has IsApp field
	ctx := SessionContext{IsApp: true}
	if !ctx.IsApp {
		t.Error("expected SessionContext.IsApp to be true")
	}

	// Assert that tool name constants are defined
	if opstool != "opstool" {
		t.Errorf("expected opstool constant to be \"opstool\", got %q", opstool)
	}
	if cascade1 != "cascade1" {
		t.Errorf("expected cascade1 constant to be \"cascade1\", got %q", cascade1)
	}
	if decide != "decide" {
		t.Errorf("expected decide constant to be \"decide\", got %q", decide)
	}
}
