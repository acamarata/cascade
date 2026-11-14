package standards

import "testing"

// TestStrictPhaseErrors verifies that strict phase returns error on fail.
func TestStrictPhaseErrors(t *testing.T) {
	p := &Policy{Mode: Strict, Quality: "enforce"}
	f := false
	if p.Allows("T1", "head1", &f) {
		t.Error("expected Allows to return false for failing gate in strict phase")
	}
}

// TestOldPhaseWarns verifies that forged phase returns warn (allows landing when gate passes).
func TestOldPhaseWarns(t *testing.T) {
	p := &Policy{Mode: Forged, Quality: "warn"}
	tr := true
	if !p.Allows("T2", "head2", &tr) {
		t.Error("expected Allows to return true for passing gate in forged phase")
	}
}

// TestEnforceSwitchDefaultWarn verifies that missing CAPP_QUALITY defaults to warn.
func TestEnforceSwitchDefaultWarn(t *testing.T) {
	p := &Policy{Mode: Strict, Quality: ""}
	tr := true
	if !p.Allows("T3", "head3", &tr) {
		t.Error("expected default warn behavior to allow landing when gate passes")
	}
}

// TestAllowsNilGateResult verifies that Allows returns false when gateResult is nil.
func TestAllowsNilGateResult(t *testing.T) {
	p := &Policy{Mode: Strict, Quality: "enforce"}
	if p.Allows("T4", "head4", nil) {
		t.Error("expected Allows to return false when gateResult is nil")
	}
}

// TestAllowsFalseGateResult verifies that Allows returns false when gateResult points to false.
func TestAllowsFalseGateResult(t *testing.T) {
	p := &Policy{Mode: Strict, Quality: "enforce"}
	f := false
	if p.Allows("T5", "head5", &f) {
		t.Error("expected Allows to return false when gateResult points to false")
	}
}
