package egress

import (
	"errors"
	"testing"
)

// TestSensitivityPassMatrix drives every tier against every combination
// of the two admission flags. The expectation column is written from the
// policy statement in SensitivityPass's doc comment, not computed from
// the implementation, so a change to either side shows up here.
func TestSensitivityPassMatrix(t *testing.T) {
	cases := []struct {
		tier            SensitivityTier
		allowRestricted bool
		allowLocalOnly  bool
		admit           bool
	}{
		{TierLocalOnly, false, false, false},
		{TierLocalOnly, true, false, false},
		{TierLocalOnly, false, true, true},
		{TierLocalOnly, true, true, true},
		{TierRestricted, false, false, false},
		{TierRestricted, true, false, true},
		{TierRestricted, false, true, false},
		{TierRestricted, true, true, true},
		{TierInternal, false, false, true},
		{TierInternal, true, true, true},
		{TierPublic, false, false, true},
		{TierPublic, true, true, true},
		// Unset and unknown resolve to restricted, so they follow the
		// restricted row exactly: refused unless AllowRestricted.
		{TierUnset, false, false, false},
		{TierUnset, true, false, true},
		{SensitivityTier("confidential"), false, false, false},
		{SensitivityTier("confidential"), true, false, true},
	}
	for _, tc := range cases {
		cfg := InterceptConfig{
			Enabled:         true,
			AllowRestricted: tc.allowRestricted,
			AllowLocalOnly:  tc.allowLocalOnly,
			Owner:           "test",
		}
		err := SensitivityPass("test.class", cfg, tc.tier)
		if tc.admit && err != nil {
			t.Errorf("tier %q restricted=%v localOnly=%v: got %v, want admitted",
				string(tc.tier), tc.allowRestricted, tc.allowLocalOnly, err)
		}
		if !tc.admit {
			if err == nil {
				t.Errorf("tier %q restricted=%v localOnly=%v: admitted, want refused",
					string(tc.tier), tc.allowRestricted, tc.allowLocalOnly)
				continue
			}
			if !errors.Is(err, ErrSensitivityViolation) {
				t.Errorf("tier %q: got %v, want ErrSensitivityViolation", string(tc.tier), err)
			}
		}
	}
}

// TestSensitivityPassAllowLocalOnly is the named check for the
// local-only rule: admitted only on a class whose registrant opted in,
// refused on every other class including the two wired here.
func TestSensitivityPassAllowLocalOnly(t *testing.T) {
	opted := InterceptConfig{Enabled: true, AllowLocalOnly: true, Owner: "test"}
	if err := SensitivityPass("controller.local", opted, TierLocalOnly); err != nil {
		t.Fatalf("local-only on an opted-in class: %v", err)
	}
	for _, class := range []EgressClass{EgressClassMCP, EgressClassHook} {
		cfg, ok := DefaultRegistry().Lookup(class)
		if !ok {
			t.Fatalf("class %q is not registered", string(class))
		}
		if cfg.AllowLocalOnly {
			t.Fatalf("class %q must not admit local-only content", string(class))
		}
		if err := SensitivityPass(class, cfg, TierLocalOnly); !errors.Is(err, ErrSensitivityViolation) {
			t.Fatalf("local-only on %q: got %v, want ErrSensitivityViolation", string(class), err)
		}
	}
}

// TestSensitivityPassAllowedTiersNarrows proves AllowedTiers can only
// ever refuse: a tier the matrix admits is still refused when the list
// omits it, and a tier the matrix refuses is not rescued by listing it.
func TestSensitivityPassAllowedTiersNarrows(t *testing.T) {
	narrow := InterceptConfig{Enabled: true, Owner: "test", AllowedTiers: []SensitivityTier{TierPublic}}
	if err := SensitivityPass("test.class", narrow, TierInternal); !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("internal against a public-only list: got %v, want refused", err)
	}
	if err := SensitivityPass("test.class", narrow, TierPublic); err != nil {
		t.Fatalf("public against a public-only list: %v", err)
	}
	widen := InterceptConfig{Enabled: true, Owner: "test", AllowedTiers: []SensitivityTier{TierLocalOnly}}
	if err := SensitivityPass("test.class", widen, TierLocalOnly); !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("listing local-only must not widen past AllowLocalOnly, got %v", err)
	}
}

// TestSensitivityTierResolve pins the fail-closed resolution rule.
func TestSensitivityTierResolve(t *testing.T) {
	for _, tier := range knownTiers {
		if got := tier.Resolve(); got != tier {
			t.Errorf("Resolve(%q) = %q, want itself", string(tier), string(got))
		}
	}
	for _, tier := range []SensitivityTier{TierUnset, "LOCAL-ONLY", "public ", "unknown"} {
		if got := tier.Resolve(); got != TierRestricted {
			t.Errorf("Resolve(%q) = %q, want restricted", string(tier), string(got))
		}
	}
}
