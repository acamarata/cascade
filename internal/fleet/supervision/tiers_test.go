package supervision

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the tier is CONFIG (R-16.54), and the config parser
//   refuses rather than guesses.
// SPORT: fleet.supervision tier-config tests (ADD) — P1-E18-W4-S39-T3.

// TestAMissingSectionIsTierOne holds the safe default. Tier 1 is the least
// autonomous of the three, so the value an operator gets by writing
// nothing is also the value that supervises most.
func TestAMissingSectionIsTierOne(t *testing.T) {
	for name, extra := range map[string]map[string]interface{}{
		"nil tree":        nil,
		"no fleet table":  {"conductor": map[string]interface{}{}},
		"fleet not a map": {"fleet": "nonsense"},
		"no supervision":  {"fleet": map[string]interface{}{}},
	} {
		tier, divergent, err := ParseSupervisionConfig(extra)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if tier != TierHookMediated {
			t.Errorf("%s: tier = %v, want tier 1", name, tier)
		}
		if !divergent {
			t.Errorf("%s: divergent = false, want true — no section was present", name)
		}
	}
}

// TestEachTierParses covers the three values an operator may write.
func TestEachTierParses(t *testing.T) {
	for n, want := range map[int64]Tier{
		1: TierHookMediated, 2: TierPTYAttached, 3: TierSuggestOnly,
	} {
		extra := map[string]interface{}{
			"fleet": map[string]interface{}{
				"supervision": map[string]interface{}{"tier": n},
			},
		}
		tier, divergent, err := ParseSupervisionConfig(extra)
		if err != nil {
			t.Fatalf("tier=%d: %v", n, err)
		}
		if tier != want {
			t.Errorf("tier=%d parsed as %v, want %v", n, tier, want)
		}
		if divergent {
			t.Errorf("tier=%d reported divergent; the section WAS present", n)
		}
		if !tier.Valid() {
			t.Errorf("tier=%d is not Valid()", n)
		}
		if tier.String() == "tier-unset" {
			t.Errorf("tier=%d renders as unset", n)
		}
	}
}

// TestAnUnrecognisedTierIsRefused is the rule that matters most here. An
// operator who wrote `tier = 4` meant something, and guessing which of
// three things they meant is worse than refusing the file — especially
// when the cheapest guess (fall back to the default) would silently give
// them LESS supervision than they asked for.
func TestAnUnrecognisedTierIsRefused(t *testing.T) {
	for name, v := range map[string]interface{}{
		"above the range": int64(4),
		"zero":            int64(0),
		"negative":        int64(-1),
		"a string":        "two",
		"a bool":          true,
		"a float":         2.0,
	} {
		extra := map[string]interface{}{
			"fleet": map[string]interface{}{
				"supervision": map[string]interface{}{"tier": v},
			},
		}
		tier, _, err := ParseSupervisionConfig(extra)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
		}
		if !errors.Is(err, ErrInvalidSupervisionConfig) {
			t.Errorf("%s: err does not wrap the sentinel", name)
		}
		if tier != 0 {
			t.Errorf("%s: a refused parse returned tier %v; a caller that ignores the error "+
				"must not receive a usable tier", name, tier)
		}
	}
}

// TestTheZeroTierIsNotAMember keeps an unset tier from reading as tier 1.
// A struct field nobody assigned must not silently become the hook-mediated
// tier, because that is a tier somebody has to choose.
func TestTheZeroTierIsNotAMember(t *testing.T) {
	var unset Tier
	if unset.Valid() {
		t.Error("the zero Tier reports Valid()")
	}
	if unset == TierHookMediated {
		t.Error("the zero Tier equals tier 1")
	}
	if unset.String() != "tier-unset" {
		t.Errorf("the zero tier renders as %q, want it to name itself unset", unset.String())
	}
}
