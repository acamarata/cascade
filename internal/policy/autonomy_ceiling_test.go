package policy

import (
	"strings"
	"testing"
)

// Purpose (this file): the auto_advance_ceiling knob — what it accepts,
//   what it refuses, and the direction it resolves when unreadable.
// SPORT: internal/policy autonomy-ceiling tests (ADD) — P1-E18-W4-S39-T2.

// TestTheCeilingDefaultsToDisabled is the property that keeps a feature
// arriving switched off: an install that never mentions the key must not
// start auto-approving because its profile's slots happen to permit it.
func TestTheCeilingDefaultsToDisabled(t *testing.T) {
	for name, tree := range map[string]map[string]interface{}{
		"no tree at all":   nil,
		"no policy table":  {"other": map[string]interface{}{}},
		"empty policy":     {"policy": map[string]interface{}{}},
		"policy with rest": {"policy": map[string]interface{}{"autonomy_profile": "strict"}},
	} {
		cfg, err := ParseConfig(tree)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.AutoAdvanceCeiling != CeilingDisabled {
			t.Errorf("%s: ceiling = %q, want disabled", name, cfg.AutoAdvanceCeiling)
		}
		if cfg.AutoAdvanceCeiling.AllowsTier1() {
			t.Errorf("%s: the default ceiling permits tier-1 auto-approval", name)
		}
	}
}

// TestTheCeilingAcceptsExactlyFourValues covers the parse, including the
// case normalisation an operator typing by hand relies on.
func TestTheCeilingAcceptsExactlyFourValues(t *testing.T) {
	for raw, want := range map[string]Ceiling{
		"disabled": CeilingDisabled, "tier1": CeilingTier1,
		"tier2": CeilingTier2, "tier3": CeilingTier3,
		" TIER1 ": CeilingTier1,
	} {
		cfg, err := ParseConfig(map[string]interface{}{
			"policy": map[string]interface{}{"auto_advance_ceiling": raw},
		})
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if cfg.AutoAdvanceCeiling != want {
			t.Errorf("%q parsed to %q, want %q", raw, cfg.AutoAdvanceCeiling, want)
		}
	}
}

// TestAnUnreadableCeilingIsAConfigError is the anti-typo rule. A value
// that silently fell back would be a misspelling choosing a behaviour
// nobody asked for, and the more permissive direction is the one that
// matters here.
func TestAnUnreadableCeilingIsAConfigError(t *testing.T) {
	for _, raw := range []interface{}{"tier9", "on", "", true, 1} {
		_, err := ParseConfig(map[string]interface{}{
			"policy": map[string]interface{}{"auto_advance_ceiling": raw},
		})
		if err == nil {
			t.Errorf("%v was accepted as a ceiling", raw)
			continue
		}
		if !strings.Contains(err.Error(), "auto_advance_ceiling") {
			t.Errorf("%v: err = %v, want it to name the key", raw, err)
		}
	}
}

// TestOnlyTier1AndAboveAllowAutoApproval covers the predicate the
// evaluator calls, including every way it can be unreadable.
func TestOnlyTier1AndAboveAllowAutoApproval(t *testing.T) {
	allowed := map[Ceiling]bool{
		CeilingDisabled: false, CeilingTier1: true, CeilingTier2: true, CeilingTier3: true,
		Ceiling(""): false, Ceiling("tier9"): false, Ceiling("TIER1"): false,
	}
	for ceiling, want := range allowed {
		if got := ceiling.AllowsTier1(); got != want {
			t.Errorf("%q.AllowsTier1() = %v, want %v", ceiling, got, want)
		}
	}
}

// TestTheCeilingNeverRendersBlank keeps audit records readable: the zero
// value must print its real meaning, not an empty field.
func TestTheCeilingNeverRendersBlank(t *testing.T) {
	if got := Ceiling("").String(); got != string(CeilingDisabled) {
		t.Errorf("zero ceiling renders %q, want %q", got, CeilingDisabled)
	}
	if got := CeilingTier2.String(); got != "tier2" {
		t.Errorf("CeilingTier2 renders %q", got)
	}
}
