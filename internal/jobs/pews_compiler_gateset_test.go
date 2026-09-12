package jobs

import "testing"

// TestEffectiveTicketGateSet_EmptyClassifierRefuses asserts an empty
// classifierDerived always refuses, regardless of declared.
func TestEffectiveTicketGateSet_EmptyClassifierRefuses(t *testing.T) {
	for _, declared := range []GateSet{nil, {}, {GateReleaseGate}} {
		if _, err := EffectiveTicketGateSet(declared, nil); err != ErrUnclassifiedFootprint {
			t.Errorf("EffectiveTicketGateSet(%v, nil) error = %v, want ErrUnclassifiedFootprint", declared, err)
		}
	}
}

// TestEffectiveTicketGateSet_IsSupersetOfBoth asserts the result always
// contains every member of both inputs, in canonical order.
func TestEffectiveTicketGateSet_IsSupersetOfBoth(t *testing.T) {
	low, _ := GateSetForRiskClass(RiskClassLow)
	normal, _ := GateSetForRiskClass(RiskClassNormal)
	high, _ := GateSetForRiskClass(RiskClassHigh)
	critical, _ := GateSetForRiskClass(RiskClassCritical)

	got, err := EffectiveTicketGateSet(low, high)
	if err != nil {
		t.Fatalf("EffectiveTicketGateSet: %v", err)
	}
	for _, g := range low {
		if !contains(got, g) {
			t.Errorf("result %v missing declared member %v", got, g)
		}
	}
	for _, g := range high {
		if !contains(got, g) {
			t.Errorf("result %v missing classifier member %v", got, g)
		}
	}
	if !gateSetEqual(got, high) {
		t.Errorf("EffectiveTicketGateSet(low, high) = %v, want %v (high is already a superset of low)", got, high)
	}
	_ = normal
	_ = critical
}

// TestEffectiveTicketGateSet_ClassifierIsUnlowerableFloor is the
// property test: for every (declared, classifierDerived) pair across
// all four risk classes, the classifier-derived set is never weakened
// -- every one of its members survives into the result.
func TestEffectiveTicketGateSet_ClassifierIsUnlowerableFloor(t *testing.T) {
	classes := []RiskClass{RiskClassLow, RiskClassNormal, RiskClassHigh, RiskClassCritical}
	for _, declaredClass := range classes {
		declared, err := GateSetForRiskClass(declaredClass)
		if err != nil {
			t.Fatalf("GateSetForRiskClass(%q): %v", declaredClass, err)
		}
		for _, classifierClass := range classes {
			classifierDerived, err := GateSetForRiskClass(classifierClass)
			if err != nil {
				t.Fatalf("GateSetForRiskClass(%q): %v", classifierClass, err)
			}
			got, err := EffectiveTicketGateSet(declared, classifierDerived)
			if err != nil {
				t.Fatalf("EffectiveTicketGateSet(%q declared, %q classifier): %v", declaredClass, classifierClass, err)
			}
			for _, g := range classifierDerived {
				if !contains(got, g) {
					t.Errorf("declared=%q classifier=%q: result %v dropped classifier-floor member %v",
						declaredClass, classifierClass, got, g)
				}
			}
		}
	}
}

// TestEffectiveTicketGateSet_CanonicalOrderDeduplicated asserts the
// result follows riskgates.go's canonical order and contains no
// duplicate, even when declared and classifierDerived overlap heavily.
func TestEffectiveTicketGateSet_CanonicalOrderDeduplicated(t *testing.T) {
	declared := GateSet{GateReleaseGate, GateFormat, GateFormat}
	classifierDerived, _ := GateSetForRiskClass(RiskClassNormal)
	got, err := EffectiveTicketGateSet(declared, classifierDerived)
	if err != nil {
		t.Fatalf("EffectiveTicketGateSet: %v", err)
	}
	seen := make(map[GateItem]int, len(got))
	for _, g := range got {
		seen[g]++
	}
	for g, n := range seen {
		if n > 1 {
			t.Errorf("gate %v appears %d times, want at most once", g, n)
		}
	}
	lastRank := -1
	for _, g := range got {
		rank := indexOf(criticalGates, g)
		if rank <= lastRank {
			t.Errorf("gate %v is out of canonical order (rank %d after %d)", g, rank, lastRank)
		}
		lastRank = rank
	}
}

func contains(gs GateSet, g GateItem) bool {
	for _, x := range gs {
		if x == g {
			return true
		}
	}
	return false
}

func indexOf(gs GateSet, g GateItem) int {
	for i, x := range gs {
		if x == g {
			return i
		}
	}
	return -1
}
