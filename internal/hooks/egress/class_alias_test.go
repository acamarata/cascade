package egress

// Purpose (this file): the egress tier is provider.SensitivityTier by type
//   identity, and ResolveTier is a former parse site (row [1]).
// Inputs: the egress constants and literal tier values.
// Outputs: assertions only.
// Constraints: identity is asserted by assignment without conversion, which
//   compiles only for an alias.
// SPORT: internal.hooks.egress.class/CHANGE (P1-SEC-19).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestEgressTierIsTheProviderType pins the alias as true type identity:
// values move between the two names with no conversion, and each egress
// constant is the provider member of the same name.
func TestEgressTierIsTheProviderType(t *testing.T) {
	passThrough := func(p provider.SensitivityTier) SensitivityTier { return p } // compiles only for an alias
	if e := passThrough(TierInternal); e != provider.SensitivityInternal {
		t.Fatalf("egress internal = %v, want the provider member", e)
	}
	for egressTier, want := range map[SensitivityTier]provider.SensitivityTier{
		TierLocalOnly: provider.SensitivityLocalOnly, TierRestricted: provider.SensitivityRestricted,
		TierInternal: provider.SensitivityInternal, TierPublic: provider.SensitivityPublic,
	} {
		if egressTier != want {
			t.Fatalf("egress %v != provider %v", egressTier, want)
		}
	}
	var zero SensitivityTier
	if zero != TierRestricted {
		t.Fatalf("the zero egress tier is %v, want restricted", zero)
	}
}

// TestFormerParseSitesFailClosed is this package's row of the former-parse-
// site table: ResolveTier. An out-of-range ("unknown") value resolves to
// local-only, the narrowest member, and the empty classification is the
// zero value, which resolves to restricted. Restoring the old
// unknown-to-restricted default turns the unknown cases red.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("egress_ResolveTier", func(t *testing.T) {
		for _, unknown := range []SensitivityTier{4, 9, 255} {
			if got := ResolveTier(unknown); got != TierLocalOnly {
				t.Fatalf("ResolveTier(%d) = %v, want local-only", uint8(unknown), got)
			}
		}
		var unset SensitivityTier
		if got := ResolveTier(unset); got != TierRestricted {
			t.Fatalf("ResolveTier(unset) = %v, want restricted", got)
		}
	})
}

// TestTierOutOfRangeRefused is the egress matrix's [R115b] row: a value
// above TierPublic is refused even by a class that admits every declared
// tier, on matrixVerdict's own range guard; internal passes as the control.
func TestTierOutOfRangeRefused(t *testing.T) {
	open := InterceptConfig{Enabled: true, Owner: "t", AllowRestricted: true, AllowLocalOnly: true}
	if err := SensitivityPass("tiers.open", open, SensitivityTier(9)); err == nil {
		t.Fatal("tier 9 passed a class that admits every declared tier")
	}
	if err := SensitivityPass("tiers.open", open, TierInternal); err != nil {
		t.Fatalf("control: internal refused: %v", err)
	}
}
