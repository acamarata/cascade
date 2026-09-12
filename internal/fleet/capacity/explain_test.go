// Purpose: TestTierSelectionExplain, the named acceptance test: Explain()
// is non-empty for every branch SelectTier can return (jump-triggered,
// reserve-applied, plain walk, probe).
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).
package capacity

import "testing"

func TestTierSelectionExplain(t *testing.T) {
	cases := []struct {
		name string
		sel  TierSelection
	}{
		{"plain walk", TierSelection{Tier: TierTwo, JumpReasonCode: JumpReasonNone}},
		{"jump triggered", TierSelection{Tier: TierZero, JumpTriggered: true, JumpReasonCode: JumpReasonRiskScore}},
		{"reserve applied without jump path note", TierSelection{Tier: TierOne, ReserveApplied: true, JumpReasonCode: JumpReasonNone}},
		{"probe selection", TierSelection{Tier: TierTwo, Probe: true, JumpReasonCode: JumpReasonNone}},
		{"jump and reserve both set", TierSelection{Tier: TierZero, JumpTriggered: true, JumpReasonCode: JumpReasonAttempt, ReserveApplied: true}},
		{"with a reason string", TierSelection{Tier: TierZero, JumpReasonCode: JumpReasonNone, Reason: "custom detail"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.sel.Explain()
			if got == "" {
				t.Fatalf("Explain() = empty string for %+v, want non-empty", c.sel)
			}
		})
	}
}

// TestJumpReasonCodeValid covers JumpReasonCode's closed membership.
func TestJumpReasonCodeValid(t *testing.T) {
	valid := []JumpReasonCode{JumpReasonNone, JumpReasonRiskScore, JumpReasonAttempt, JumpReasonMinQuality, JumpReasonExpectedTime}
	for _, c := range valid {
		if !c.Valid() {
			t.Errorf("JumpReasonCode(%q).Valid() = false, want true", c)
		}
	}
	if JumpReasonCode("bogus").Valid() {
		t.Error(`JumpReasonCode("bogus").Valid() = true, want false`)
	}
	if JumpReasonCode("").Valid() {
		t.Error(`JumpReasonCode("").Valid() = true, want false (no permissive zero value)`)
	}
}
