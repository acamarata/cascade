package egress

import "testing"

// The registry keeps restricted tiers as explicit opt-ins, while invalid tiers refuse.
// Every valid tier takes its existing branch; an out-of-range tier takes the refusal.
func TestDataClassRegistryRestrictedBranches(t *testing.T) {
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted, TierInternal, TierPublic, 9} {
		r := NewRegistry()
		err := r.Register("fold", InterceptConfig{Owner: "test", Enabled: true, AllowedTiers: []SensitivityTier{tier}})
		if tier == 9 {
			if err == nil {
				t.Fatal("invalid tier registered")
			}
			if _, ok := r.Lookup("fold"); ok {
				t.Fatal("invalid registration stored")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		cfg, ok := r.Lookup("fold")
		if !ok || len(cfg.AllowedTiers) != 1 || cfg.AllowedTiers[0] != tier {
			t.Fatal("registration changed")
		}
		err = SensitivityPass("fold", cfg, tier)
		if open := tier == TierInternal || tier == TierPublic; open != (err == nil) {
			t.Fatalf("tier %v: %v", tier, err)
		}
	}
}

func TestDataClassSensitivityRestrictedBranches(t *testing.T) {
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted, TierInternal, TierPublic, 9} {
		err := SensitivityPass("fold", InterceptConfig{Owner: "test"}, tier)
		if open := tier == TierInternal || tier == TierPublic; open != (err == nil) {
			t.Fatalf("tier %v: %v", tier, err)
		}
	}
	cfg := InterceptConfig{Owner: "test", AllowLocalOnly: true, AllowRestricted: true}
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted} {
		if err := SensitivityPass("fold", cfg, tier); err != nil {
			t.Fatal(err)
		}
	}
	if err := SensitivityPass("fold", cfg, 9); err == nil {
		t.Fatal("invalid tier admitted")
	}
}
