package inboxenv

import (
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func setCeilingTestEnv(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("USERPROFILE", d)
	t.Setenv("CASCADE_HOME", d)
}

// TestModelCeilingConstant verifies ModelCeiling equals SensitivityInternal.
func TestModelCeilingConstant(t *testing.T) {
	setCeilingTestEnv(t)
	if ModelCeiling != provider.SensitivityInternal {
		t.Errorf("ModelCeiling = %v, want %v", ModelCeiling, provider.SensitivityInternal)
	}
}

// TestModelAdmitsInvalidTierFalse verifies ModelAdmits for all tier cases.
func TestModelAdmitsInvalidTierFalse(t *testing.T) {
	setCeilingTestEnv(t)
	cases := []struct {
		name string
		tier provider.SensitivityTier
		want bool
	}{
		{"internal", provider.SensitivityInternal, true},
		{"public", provider.SensitivityPublic, true},
		{"restricted", provider.SensitivityRestricted, false},
		{"local-only", provider.SensitivityLocalOnly, false},
		{"invalid", provider.SensitivityTier(99), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelAdmits(tc.tier)
			if got != tc.want {
				t.Errorf("ModelAdmits(%v) = %v, want %v", tc.tier, got, tc.want)
			}
		})
	}
}
