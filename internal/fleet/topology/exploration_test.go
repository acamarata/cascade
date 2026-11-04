package topology

import (
	"math"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

var ranked = []provider.SensitivityTier{
	provider.SensitivityPublic, provider.SensitivityInternal,
	provider.SensitivityRestricted, provider.SensitivityLocalOnly,
}

// TestTierAdmitsByRank: a 4x4 grid by rank, invalid requests read as
// local-only and a bad or zero Ceiling admits nothing.
func TestTierAdmitsByRank(t *testing.T) {
	for ci, ct := range ranked {
		for ri, rt := range ranked {
			c := Ceiling{Tier: ct, Reason: CeilingLaneType}
			if got, want := TierAdmits(rt, c), ri <= ci; got != want {
				t.Errorf("TierAdmits(%s, %s) = %v, want %v", rt, ct, got, want)
			}
		}
	}
	local := Ceiling{Tier: provider.SensitivityLocalOnly, Reason: CeilingLocalEndpoint}
	restricted := Ceiling{Tier: provider.SensitivityRestricted, Reason: CeilingLaneType}
	for _, bad := range []provider.SensitivityTier{4, 255} {
		if !TierAdmits(bad, local) || TierAdmits(bad, restricted) {
			t.Errorf("request tier %d does not read as local-only", bad)
		}
	}
	for _, c := range []Ceiling{{}, {Tier: 9, Reason: CeilingLaneType}, {Tier: provider.SensitivityPublic, Reason: "nope"}} {
		for _, rt := range ranked {
			if TierAdmits(rt, c) {
				t.Errorf("ceiling %+v admits %s", c, rt)
			}
		}
	}
}

// TestExplorationEligible: public, admitted, known terms and a tolerance
// no riskier than the incumbent explore; each missing condition refuses.
func TestExplorationEligible(t *testing.T) {
	pub := Ceiling{Tier: provider.SensitivityPublic, Reason: CeilingBillingCap}
	known := ProviderPolicy{Flags: knownPolicy}
	if !ExplorationEligible(provider.SensitivityPublic, pub, known, 0.1, 0.2) {
		t.Fatal("eligible candidate refused")
	}
	if !ExplorationEligible(provider.SensitivityPublic, pub, known, 0.2, 0.2) {
		t.Fatal("equal tolerance refused")
	}
	cases := map[string]bool{
		"riskier":           ExplorationEligible(provider.SensitivityPublic, pub, known, 0.3, 0.2),
		"nan":               ExplorationEligible(provider.SensitivityPublic, pub, known, math.NaN(), 0.2),
		"training unknown":  ExplorationEligible(provider.SensitivityPublic, pub, ProviderPolicy{Flags: map[string]string{"retention": "30d"}}, 0, 1),
		"retention unknown": ExplorationEligible(provider.SensitivityPublic, pub, ProviderPolicy{Flags: map[string]string{"training": "false", "retention": "unknown"}}, 0, 1),
		"zero ceiling":      ExplorationEligible(provider.SensitivityPublic, Ceiling{}, known, 0, 1),
		"internal request":  ExplorationEligible(provider.SensitivityInternal, Ceiling{Tier: provider.SensitivityInternal, Reason: CeilingLaneType}, known, 0, 1),
	}
	for name, got := range cases {
		if got {
			t.Errorf("%s: explored", name)
		}
	}
}

// TestConfidentialRequestNeverExplores: no request above public explores,
// whatever the candidate's ceiling, policy or tolerance.
func TestConfidentialRequestNeverExplores(t *testing.T) {
	top := Ceiling{Tier: provider.SensitivityLocalOnly, Reason: CeilingLocalEndpoint}
	for _, req := range []provider.SensitivityTier{provider.SensitivityInternal, provider.SensitivityRestricted, provider.SensitivityLocalOnly, 4, 255} {
		if ExplorationEligible(req, top, ProviderPolicy{Flags: knownPolicy}, 0, 1) {
			t.Errorf("request tier %d explored", req)
		}
	}
}
