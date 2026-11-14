package broker

import "testing"

// TestTypesCompile verifies that all routing types, tier constants,
// and checker interfaces are defined and usable.
func TestTypesCompile(t *testing.T) {
	// Verify Tier constants exist and have distinct values.
	tiers := []Tier{
		TierSys0,
		TierSys1,
		TierSys2Rung1,
		TierSys2Rung2,
		TierSys2Rung3,
		TierSys2Rung4,
		TierSys2Rung5,
		TierSys2Rung6,
		TierSys2Rung7,
		TierSys2Rung8,
	}
	seen := make(map[Tier]bool)
	for _, tr := range tiers {
		if seen[tr] {
			t.Errorf("duplicate tier value: %d", tr)
		}
		seen[tr] = true
	}

	// Verify RouteRequest fields compile.
	req := RouteRequest{
		Input:  "test input",
		IsCode: true,
	}
	if req.Input != "test input" {
		t.Error("RouteRequest.Input mismatch")
	}
	if !req.IsCode {
		t.Error("RouteRequest.IsCode mismatch")
	}

	// Verify RouteResult fields compile.
	res := RouteResult{
		Tier:    TierSys0,
		Refused: false,
	}
	if res.Tier != TierSys0 {
		t.Error("RouteResult.Tier mismatch")
	}
	if res.Refused {
		t.Error("RouteResult.Refused mismatch")
	}

	// Verify QuotaChecker interface compiles by assigning nil.
	var _ = (QuotaChecker)(nil)

	// Verify RPMChecker interface compiles by assigning nil.
	var _ = (RPMChecker)(nil)
}
