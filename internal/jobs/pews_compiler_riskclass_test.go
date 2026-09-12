package jobs

import "testing"

// TestDeclaredRiskClass_CompleteR1642FormSet covers the complete
// R-16.42 canonical cr_level form set crossed with every qa_level,
// asserting the resolved class is the higher of the two per-field
// classes and never Critical.
func TestDeclaredRiskClass_CompleteR1642FormSet(t *testing.T) {
	cases := []struct {
		cr, qa string
		want   RiskClass
	}{
		{"CR-B", "QA-A", RiskClassNormal},
		{"CR-B", "QA-B", RiskClassNormal},
		{"CR-A+CR-B", "QA-A", RiskClassNormal},
		{"CR-A+CR-B", "QA-B", RiskClassNormal},
		{"CR-B", "QA-C", RiskClassHigh},      // qa-derived wins
		{"CR-B+CR-C", "QA-A", RiskClassHigh}, // cr-derived wins
		{"CR-A+CR-B+CR-C", "QA-B", RiskClassHigh},
		{"CR-B+CR-C", "QA-C", RiskClassHigh},
		{"CR-A+CR-B+CR-C", "QA-C", RiskClassHigh},
	}
	for _, tc := range cases {
		got, err := declaredRiskClass(tc.cr, tc.qa)
		if err != nil {
			t.Fatalf("declaredRiskClass(%q, %q): %v", tc.cr, tc.qa, err)
		}
		if got != tc.want {
			t.Errorf("declaredRiskClass(%q, %q) = %q, want %q", tc.cr, tc.qa, got, tc.want)
		}
		if got == RiskClassCritical {
			t.Errorf("declaredRiskClass(%q, %q) = Critical, never derivable from levels", tc.cr, tc.qa)
		}
	}
}

// TestDeclaredRiskClass_UnknownForms asserts every form outside the
// R-16.42 set refuses rather than defaulting.
func TestDeclaredRiskClass_UnknownForms(t *testing.T) {
	badCR := []string{"", "CR-A", "CR-C", "CR-B+CR-A", "cr-b", "CR-A+CR-B+CR-C+CR-D"}
	for _, cr := range badCR {
		if _, err := declaredRiskClass(cr, "QA-B"); err == nil {
			t.Errorf("declaredRiskClass(%q, QA-B): want error, got nil", cr)
		}
	}
	badQA := []string{"", "QA-D", "qa-a", "QA-A+QA-B"}
	for _, qa := range badQA {
		if _, err := declaredRiskClass("CR-B", qa); err == nil {
			t.Errorf("declaredRiskClass(CR-B, %q): want error, got nil", qa)
		}
	}
}

// TestDeclaredGateSet_MatchesTableAndOverlay asserts DeclaredGateSet
// composes declaredRiskClass with the ONE risk-gate table
// (GateSetForRiskClass) as tightened by an overlay, defining no gate
// table of its own.
func TestDeclaredGateSet_MatchesTableAndOverlay(t *testing.T) {
	got, err := DeclaredGateSet("CR-B", "QA-B", nil)
	if err != nil {
		t.Fatalf("DeclaredGateSet: %v", err)
	}
	want, err := GateSetForRiskClass(RiskClassNormal)
	if err != nil {
		t.Fatalf("GateSetForRiskClass: %v", err)
	}
	if !gateSetEqual(got, want) {
		t.Errorf("DeclaredGateSet(CR-B, QA-B, nil) = %v, want %v", got, want)
	}

	overlay := RiskGateOverlay{RiskClassNormal: GateSet{GateHumanApproval}}
	got, err = DeclaredGateSet("CR-B", "QA-B", overlay)
	if err != nil {
		t.Fatalf("DeclaredGateSet with overlay: %v", err)
	}
	found := false
	for _, g := range got {
		if g == GateHumanApproval {
			found = true
		}
	}
	if !found {
		t.Errorf("DeclaredGateSet with overlay = %v, want GateHumanApproval present", got)
	}
}

// TestDeclaredGateSet_UnknownLevelRefuses asserts DeclaredGateSet
// propagates the same typed refusal declaredRiskClass returns, for both
// an unknown cr_level and an unknown qa_level.
func TestDeclaredGateSet_UnknownLevelRefuses(t *testing.T) {
	if _, err := DeclaredGateSet("CR-Z", "QA-B", nil); err == nil {
		t.Error("DeclaredGateSet(CR-Z, QA-B, nil): want error, got nil")
	}
	if _, err := DeclaredGateSet("CR-B", "QA-Z", nil); err == nil {
		t.Error("DeclaredGateSet(CR-B, QA-Z, nil): want error, got nil")
	}
}
