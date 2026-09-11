package jobs

import "testing"

func gateSetHas(set GateSet, item GateItem) bool {
	for _, g := range set {
		if g == item {
			return true
		}
	}
	return false
}

// assertGateSetContains fails the test unless set contains every item.
func assertGateSetContains(t *testing.T, className string, set GateSet, items ...GateItem) {
	t.Helper()
	for _, item := range items {
		if !gateSetHas(set, item) {
			t.Fatalf("%s gate set missing %v", className, item)
		}
	}
}

// assertGateSetAdditive fails the test unless every item of lower is
// also present in higher (the additive-over-the-severity-order rule).
func assertGateSetAdditive(t *testing.T, higherName string, higher, lower GateSet) {
	t.Helper()
	for _, item := range lower {
		if !gateSetHas(higher, item) {
			t.Fatalf("%s gate set must be additive over the lower class; missing %v", higherName, item)
		}
	}
}

// TestGateSetForRiskClass_DecidedTableVerbatim asserts the four-class
// gate-set table is the DECIDED VERBATIM mapping: each class's own
// named additions, plus every lower class's set (additive per risk.go's
// severity ordering).
func TestGateSetForRiskClass_DecidedTableVerbatim(t *testing.T) {
	low, err := GateSetForRiskClass(RiskClassLow)
	if err != nil {
		t.Fatalf("low: %v", err)
	}
	assertGateSetContains(t, "low", low, GateFormat, GateStatic, GateTargetedVerification)
	if len(low) != 3 {
		t.Fatalf("low gate set = %v, want exactly the 3 DECIDED items", low)
	}

	normal, err := GateSetForRiskClass(RiskClassNormal)
	if err != nil {
		t.Fatalf("normal: %v", err)
	}
	assertGateSetContains(t, "normal", normal, GateBuild, GateLint, GateTargetedTests, GateCodeReview, GateIntegrationChecks)
	assertGateSetAdditive(t, "normal", normal, low)

	high, err := GateSetForRiskClass(RiskClassHigh)
	if err != nil {
		t.Fatalf("high: %v", err)
	}
	assertGateSetContains(t, "high", high, GateIndependentQA, GateAdversarialReview, GateAffectedFullIntegration, GateCleanNodeVerification)
	assertGateSetAdditive(t, "high", high, normal)

	critical, err := GateSetForRiskClass(RiskClassCritical)
	if err != nil {
		t.Fatalf("critical: %v", err)
	}
	assertGateSetContains(t, "critical", critical, GateHumanApproval, GateRollbackEvidence, GateReleaseGate)
	assertGateSetAdditive(t, "critical", critical, high)
}

func TestGateSetForRiskClass_UnknownFailsClosed(t *testing.T) {
	if _, err := GateSetForRiskClass(RiskClass("bogus")); err == nil {
		t.Fatal("GateSetForRiskClass(bogus) = nil error, want a typed error, never a permissive default set")
	}
}

func TestGateSetForRiskClass_DefensiveCopy(t *testing.T) {
	first, _ := GateSetForRiskClass(RiskClassLow)
	first[0] = "mutated"
	second, _ := GateSetForRiskClass(RiskClassLow)
	if second[0] == "mutated" {
		t.Fatal("GateSetForRiskClass must return a defensive copy, not a shared slice")
	}
}
