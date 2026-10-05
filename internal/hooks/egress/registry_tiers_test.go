package egress_test

// Purpose (this file): the Register tier rules (P1-BF-R115 as corrected by
//   R121): Register refuses only out-of-range tiers, and AllowedTiers only
//   narrows a class.
// Inputs: real registries and the conductor class's InterceptConfig shape.
// Outputs: assertions only.
// Constraints: imports nothing that registers into the default registry at
//   init (internal/conductor, nodes and sync all do, which would break
//   classes_test.go's exact class count in this test binary); the real
//   RegisterEgressClassConductor call is exercised by
//   internal/conductor/sensitivity_test.go. Every refusal has a control.
// SPORT: internal.hooks.egress.registry/CHANGE (P1-SEC-19).

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegisterTierRules pins what Register refuses and what AllowedTiers
// may do: an out-of-range tier refuses with KindInvalidInput naming the
// class; the conductor class shape (all four tiers, both flags) still
// registers;
// and AllowedTiers only narrows, so a class listing restricted without
// AllowRestricted still refuses restricted content.
func TestRegisterTierRules(t *testing.T) {
	reg := egress.NewRegistry()
	bad := egress.InterceptConfig{Enabled: true, Owner: "t", AllowedTiers: []egress.SensitivityTier{egress.SensitivityTier(9)}}
	err := reg.Register("tiers.bad", bad)
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Register with tier 9 = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), `"tiers.bad"`) || !strings.Contains(err.Error(), "9") {
		t.Fatalf("refusal %q must name the class and the value", err)
	}
	conductorShape := egress.InterceptConfig{
		Enabled: true, Owner: "t", AllowRestricted: true, AllowLocalOnly: true,
		AllowedTiers: []egress.SensitivityTier{egress.TierLocalOnly, egress.TierRestricted, egress.TierInternal, egress.TierPublic},
	}
	if err := reg.Register("tiers.conductor-shape", conductorShape); err != nil {
		t.Fatalf("the conductor class shape no longer registers: %v", err)
	}
	narrow := egress.InterceptConfig{Enabled: true, Owner: "t", AllowedTiers: []egress.SensitivityTier{egress.TierRestricted}}
	if err := reg.Register("tiers.narrow", narrow); err != nil {
		t.Fatalf("a class listing restricted was refused at registration: %v", err)
	}
	if err := egress.SensitivityPass("tiers.narrow", narrow, egress.TierRestricted); err == nil {
		t.Fatal("AllowedTiers {restricted} admitted restricted content without AllowRestricted")
	}
	if err := egress.SensitivityPass("tiers.narrow", narrow, egress.TierInternal); err == nil {
		t.Fatal("AllowedTiers {restricted} admitted internal content it does not list")
	}
	narrow.AllowRestricted = true
	if err := egress.SensitivityPass("tiers.narrow", narrow, egress.TierRestricted); err != nil {
		t.Fatalf("control: restricted with AllowRestricted and listed was refused: %v", err)
	}
}
