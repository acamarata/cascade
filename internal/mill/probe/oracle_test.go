package probe

import (
	"testing"
)

// TestOraclesRunInCode verifies that oracles execute purely in code
// without requiring any external model or service.
func TestOraclesRunInCode(t *testing.T) {
	state := State{
		ConsoleErrors:    []string{"TypeError: x is undefined"},
		NetworkResponses: map[string]int{"/api/data": 500},
		OverflowElements: []string{"#sidebar"},
		A11yViolations:   []string{"missing-alt"},
		Steps:            []Step{{Action: "click", Target: "#btn"}},
	}

	oracles := []Oracle{
		ConsoleErrorOracle{},
		AccessibilityOracle{},
		NetworkErrorOracle{},
		LayoutOverflowOracle{},
	}

	findings := RunOracles(oracles, state)

	if len(findings) != 4 {
		t.Fatalf("expected 4 findings, got %d", len(findings))
	}

	oracleNames := map[string]bool{}
	for _, f := range findings {
		oracleNames[f.Oracle] = true
		if len(f.Steps) != 1 || f.Steps[0].Action != "click" {
			t.Errorf("unexpected steps in finding: %+v", f.Steps)
		}
	}

	for _, name := range []string{"console-error", "accessibility", "network-error", "layout-overflow"} {
		if !oracleNames[name] {
			t.Errorf("missing finding from oracle %q", name)
		}
	}
}

// TestConsoleErrorOracleDetects verifies that ConsoleErrorOracle detects console errors.
func TestConsoleErrorOracleDetects(t *testing.T) {
	o := ConsoleErrorOracle{}
	state := State{ConsoleErrors: []string{"Uncaught ReferenceError"}}
	findings := o.Check(state)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Oracle != "console-error" {
		t.Errorf("wrong oracle name: %s", findings[0].Oracle)
	}
}

// TestNetworkErrorOracleFlags verifies that NetworkErrorOracle flags 4xx and 5xx responses.
func TestNetworkErrorOracleFlags(t *testing.T) {
	o := NetworkErrorOracle{}
	state := State{
		NetworkResponses: map[string]int{
			"/ok":       200,
			"/notfound": 404,
			"/broken":   503,
		},
	}
	findings := o.Check(state)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	for _, f := range findings {
		if f.Oracle != "network-error" {
			t.Errorf("wrong oracle name: %s", f.Oracle)
		}
	}
}

// TestLayoutOverflowOracleDetects verifies that LayoutOverflowOracle detects overflow.
func TestLayoutOverflowOracleDetects(t *testing.T) {
	o := LayoutOverflowOracle{}
	state := State{OverflowElements: []string{"#header", "#footer"}}
	findings := o.Check(state)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
}

// TestNoModelNoOracleFallback verifies that oracles run without any model dependency.
func TestNoModelNoOracleFallback(t *testing.T) {
	state := State{}
	oracles := []Oracle{
		ConsoleErrorOracle{},
		AccessibilityOracle{},
		NetworkErrorOracle{},
		LayoutOverflowOracle{},
	}

	// RunOracles must succeed with empty state and no model available.
	findings := RunOracles(oracles, state)
	if findings == nil {
		findings = []Finding{}
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for empty state, got %d", len(findings))
	}
}
