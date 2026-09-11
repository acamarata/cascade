package jobs

import (
	"testing"
)

var allRiskClasses = []RiskClass{RiskClassLow, RiskClassNormal, RiskClassHigh, RiskClassCritical}

// TestEffectiveGateSet_EmptyOverlayMatchesTable asserts EffectiveGateSet
// with a nil/empty overlay reproduces GateSetForRiskClass exactly, for
// every class.
func TestEffectiveGateSet_EmptyOverlayMatchesTable(t *testing.T) {
	for _, rc := range allRiskClasses {
		want, err := GateSetForRiskClass(rc)
		if err != nil {
			t.Fatalf("GateSetForRiskClass(%q): %v", rc, err)
		}
		got, err := EffectiveGateSet(rc, nil)
		if err != nil {
			t.Fatalf("EffectiveGateSet(%q, nil): %v", rc, err)
		}
		if !gateSetEqual(got, want) {
			t.Errorf("EffectiveGateSet(%q, nil) = %v, want %v", rc, got, want)
		}
	}
}

// TestEffectiveGateSet_IsSupersetOfTable is the property test: for
// every class and a representative set of overlay values, the
// effective set is always a superset of the table's own result --
// TIGHTENING-ONLY, by construction of RiskGateOverlay's additive type.
func TestEffectiveGateSet_IsSupersetOfTable(t *testing.T) {
	overlays := []RiskGateOverlay{
		nil,
		{},
		{RiskClassLow: GateSet{GateHumanApproval}},
		{RiskClassNormal: GateSet{GateIndependentQA, GateAdversarialReview}},
		{RiskClassHigh: GateSet{GateReleaseGate}},
		{RiskClassCritical: GateSet{GateFormat}}, // already present: must stay a no-op, not a duplicate
		{
			RiskClassLow:    GateSet{GateBuild},
			RiskClassNormal: GateSet{GateHumanApproval},
			RiskClassHigh:   GateSet{GateRollbackEvidence},
		},
	}
	for _, rc := range allRiskClasses {
		table, err := GateSetForRiskClass(rc)
		if err != nil {
			t.Fatalf("GateSetForRiskClass(%q): %v", rc, err)
		}
		for _, ov := range overlays {
			effective, err := EffectiveGateSet(rc, ov)
			if err != nil {
				t.Fatalf("EffectiveGateSet(%q, %v): %v", rc, ov, err)
			}
			if !isSuperset(effective, table) {
				t.Errorf("EffectiveGateSet(%q, %v) = %v is not a superset of the table result %v", rc, ov, effective, table)
			}
		}
	}
}

// TestEffectiveGateSet_IsSupersetOfCriticalFloor asserts the R-21.182
// Critical floor's own gate set (criticalGates, the maximal DECIDED
// set) can never be undercut through this ticket's surface: whatever
// overlay is in effect, EffectiveGateSet(RiskClassCritical, ov) remains
// a superset of the floor's gate set. No overlay key can express a
// removal, so this holds for every overlay value including one that
// only touches lower classes.
func TestEffectiveGateSet_IsSupersetOfCriticalFloor(t *testing.T) {
	floor, err := GateSetForRiskClass(RiskClassCritical)
	if err != nil {
		t.Fatalf("GateSetForRiskClass(critical): %v", err)
	}
	overlays := []RiskGateOverlay{
		nil,
		{RiskClassLow: GateSet{GateBuild}},
		{RiskClassCritical: GateSet{GateFormat, GateBuild}},
	}
	for _, ov := range overlays {
		effective, err := EffectiveGateSet(RiskClassCritical, ov)
		if err != nil {
			t.Fatalf("EffectiveGateSet(critical, %v): %v", ov, err)
		}
		if !isSuperset(effective, floor) {
			t.Errorf("EffectiveGateSet(critical, %v) = %v is not a superset of the Critical floor's gate set %v", ov, effective, floor)
		}
	}
}

// TestEffectiveGateSet_OrderingTableThenOverlay asserts the table's
// canonical order comes first, followed by the overlay's declared
// order, de-duplicated.
func TestEffectiveGateSet_OrderingTableThenOverlay(t *testing.T) {
	ov := RiskGateOverlay{RiskClassLow: GateSet{GateHumanApproval, GateRollbackEvidence, GateFormat}}
	got, err := EffectiveGateSet(RiskClassLow, ov)
	if err != nil {
		t.Fatalf("EffectiveGateSet: %v", err)
	}
	want := GateSet{GateFormat, GateStatic, GateTargetedVerification, GateHumanApproval, GateRollbackEvidence}
	if !gateSetEqual(got, want) {
		t.Fatalf("EffectiveGateSet ordering = %v, want %v (table order, then overlay order, GateFormat de-duplicated)", got, want)
	}
}

// TestEffectiveGateSet_UnknownRiskClass asserts ErrUnknownRiskClass-style
// fail-closed propagation from GateSetForRiskClass, unchanged.
func TestEffectiveGateSet_UnknownRiskClass(t *testing.T) {
	_, err := EffectiveGateSet(RiskClass("bogus"), nil)
	if err == nil {
		t.Fatal("EffectiveGateSet(bogus, nil): expected an error, got nil")
	}
}

// TestParseGateItem_Valid asserts every criticalGates member round-trips.
func TestParseGateItem_Valid(t *testing.T) {
	for _, g := range criticalGates {
		got, err := ParseGateItem(string(g))
		if err != nil {
			t.Fatalf("ParseGateItem(%q): %v", g, err)
		}
		if got != g {
			t.Errorf("ParseGateItem(%q) = %q, want %q", g, got, g)
		}
	}
}

// TestParseGateItem_Unknown asserts a fail-closed refusal for a name
// outside the DECIDED vocabulary.
func TestParseGateItem_Unknown(t *testing.T) {
	if _, err := ParseGateItem("not-a-real-gate"); err == nil {
		t.Fatal("ParseGateItem(not-a-real-gate): expected an error, got nil")
	}
}

// TestBuildRiskGateOverlay_Valid asserts the raw-string cross-package
// shape internal/policy hands over converts correctly, validating each
// gate-step name against criticalGates.
func TestBuildRiskGateOverlay_Valid(t *testing.T) {
	ov, err := BuildRiskGateOverlay(map[string][]string{
		"low":      {"human_approval"},
		"critical": {"format", "static"},
	})
	if err != nil {
		t.Fatalf("BuildRiskGateOverlay: %v", err)
	}
	if !gateSetEqual(ov[RiskClassLow], GateSet{GateHumanApproval}) {
		t.Errorf("ov[low] = %v, want [human_approval]", ov[RiskClassLow])
	}
	if len(ov[RiskClassCritical]) != 2 {
		t.Errorf("ov[critical] = %v, want 2 entries", ov[RiskClassCritical])
	}
}

// TestBuildRiskGateOverlay_Empty asserts an empty/nil raw map yields a
// nil overlay, not an error.
func TestBuildRiskGateOverlay_Empty(t *testing.T) {
	ov, err := BuildRiskGateOverlay(nil)
	if err != nil || ov != nil {
		t.Fatalf("BuildRiskGateOverlay(nil) = (%v, %v), want (nil, nil)", ov, err)
	}
}

// TestBuildRiskGateOverlay_UnknownClass asserts a fail-closed refusal
// for a class name outside the four RiskClass members.
func TestBuildRiskGateOverlay_UnknownClass(t *testing.T) {
	if _, err := BuildRiskGateOverlay(map[string][]string{"extreme": {"format"}}); err == nil {
		t.Fatal("BuildRiskGateOverlay with an unknown class: expected an error, got nil")
	}
}

// TestBuildRiskGateOverlay_UnknownGateStep asserts a fail-closed refusal
// for an unparseable gate-step name.
func TestBuildRiskGateOverlay_UnknownGateStep(t *testing.T) {
	if _, err := BuildRiskGateOverlay(map[string][]string{"low": {"not-a-real-gate"}}); err == nil {
		t.Fatal("BuildRiskGateOverlay with an unknown gate step: expected an error, got nil")
	}
}

func gateSetEqual(a, b GateSet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSuperset(superset, subset GateSet) bool {
	have := make(map[GateItem]bool, len(superset))
	for _, g := range superset {
		have[g] = true
	}
	for _, g := range subset {
		if !have[g] {
			return false
		}
	}
	return true
}
