package jobs

import "testing"

// TestRaiseRiskClass_ClampsLowerObserved asserts an observed class
// below prior is clamped to prior -- never an error, never a
// lowering -- and reports no escalation.
func TestRaiseRiskClass_ClampsLowerObserved(t *testing.T) {
	got, esc, err := RaiseRiskClass(RiskClassHigh, RiskClassLow, nil)
	if err != nil {
		t.Fatalf("RaiseRiskClass: %v", err)
	}
	if got != RiskClassHigh {
		t.Errorf("RaiseRiskClass clamped result = %q, want prior (high)", got)
	}
	if !isZeroEscalation(esc) {
		t.Errorf("RaiseRiskClass on a clamp: got non-zero escalation %+v", esc)
	}
}

// TestRaiseRiskClass_Unchanged asserts an observed class equal to prior
// also reports no escalation.
func TestRaiseRiskClass_Unchanged(t *testing.T) {
	got, esc, err := RaiseRiskClass(RiskClassNormal, RiskClassNormal, nil)
	if err != nil {
		t.Fatalf("RaiseRiskClass: %v", err)
	}
	if got != RiskClassNormal {
		t.Errorf("RaiseRiskClass = %q, want normal", got)
	}
	if !isZeroEscalation(esc) {
		t.Errorf("RaiseRiskClass on no change: got non-zero escalation %+v", esc)
	}
}

// TestRaiseRiskClass_Escalates asserts an increase returns the derived
// class and a populated RiskEscalation naming the prior class's
// effective gate set as InvalidatedGateSet, with RequiresExpandedLease
// true.
func TestRaiseRiskClass_Escalates(t *testing.T) {
	ov := RiskGateOverlay{RiskClassLow: GateSet{GateHumanApproval}}
	got, esc, err := RaiseRiskClass(RiskClassLow, RiskClassHigh, ov)
	if err != nil {
		t.Fatalf("RaiseRiskClass: %v", err)
	}
	if got != RiskClassHigh {
		t.Errorf("RaiseRiskClass = %q, want high", got)
	}
	if esc.From != RiskClassLow || esc.To != RiskClassHigh {
		t.Errorf("RiskEscalation{From,To} = {%q,%q}, want {low,high}", esc.From, esc.To)
	}
	if !esc.RequiresExpandedLease {
		t.Error("RiskEscalation.RequiresExpandedLease = false, want true on a real escalation")
	}
	wantInvalidated, err := EffectiveGateSet(RiskClassLow, ov)
	if err != nil {
		t.Fatalf("EffectiveGateSet: %v", err)
	}
	if !gateSetEqual(esc.InvalidatedGateSet, wantInvalidated) {
		t.Errorf("InvalidatedGateSet = %v, want EffectiveGateSet(prior, ov) = %v", esc.InvalidatedGateSet, wantInvalidated)
	}
}

// TestRaiseRiskClass_UnknownPrior asserts ErrUnknownRiskClass-style
// fail-closed propagation for an unrecognised prior class.
func TestRaiseRiskClass_UnknownPrior(t *testing.T) {
	if _, _, err := RaiseRiskClass(RiskClass("bogus"), RiskClassNormal, nil); err == nil {
		t.Fatal("RaiseRiskClass with unknown prior: expected an error, got nil")
	}
}

// TestRaiseRiskClass_UnknownObserved asserts the same for an
// unrecognised observed class.
func TestRaiseRiskClass_UnknownObserved(t *testing.T) {
	if _, _, err := RaiseRiskClass(RiskClassNormal, RiskClass("bogus"), nil); err == nil {
		t.Fatal("RaiseRiskClass with unknown observed: expected an error, got nil")
	}
}

// TestRaiseRiskClass_Monotonic is a property test across every class
// pair: the result is never below prior in severity, matching R-21.146.
func TestRaiseRiskClass_Monotonic(t *testing.T) {
	for _, prior := range allRiskClasses {
		for _, observed := range allRiskClasses {
			got, _, err := RaiseRiskClass(prior, observed, nil)
			if err != nil {
				t.Fatalf("RaiseRiskClass(%q, %q): %v", prior, observed, err)
			}
			if riskRank[got] < riskRank[prior] {
				t.Errorf("RaiseRiskClass(%q, %q) = %q, which is below prior", prior, observed, got)
			}
		}
	}
}

func isZeroEscalation(esc RiskEscalation) bool {
	return esc.From == "" && esc.To == "" && len(esc.InvalidatedGateSet) == 0 && !esc.RequiresExpandedLease
}
