package provider_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestDataClassNameRoundTrip pins the stored vocabulary independently of tier names.
func TestDataClassNameRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{
		{"public", provider.SensitivityPublic}, {"internal", provider.SensitivityInternal},
		{"confidential", provider.SensitivityRestricted}, {"secret", provider.SensitivityLocalOnly},
	} {
		name, err := provider.DataClassName(tc.tier)
		if err != nil || name != tc.word {
			t.Fatalf("encode %v = %q, %v", tc.tier, name, err)
		}
		tier, err := provider.ParseDataClassName(tc.word)
		if err != nil || tier != tc.tier {
			t.Fatalf("decode %q = %v, %v", tc.word, tier, err)
		}
		if provider.TierOrLocalOnly(new(tc.tier)) != tc.tier {
			t.Fatal("valid tier changed")
		}
	}
	for _, word := range []string{"", "Secret", " secret", "top-secret", "local-only", "restricted"} {
		tier, err := provider.ParseDataClassName(word)
		if tier != provider.SensitivityLocalOnly || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), strconv.Quote(word)) {
			t.Fatalf("decode %q = %v, %v", word, tier, err)
		}
	}
	if _, err := provider.DataClassName(provider.SensitivityTier(9)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("invalid tier: %v", err)
	}
	for _, p := range []*provider.SensitivityTier{nil, new(provider.SensitivityTier(9))} {
		if provider.TierOrLocalOnly(p) != provider.SensitivityLocalOnly {
			t.Fatal("unset or invalid tier did not fail closed")
		}
	}
}

func TestAgentJobSpecZeroDataClassReadsRestricted(t *testing.T) {
	spec := provider.AgentJobSpec{}
	if spec.DataClass != provider.SensitivityRestricted {
		t.Fatalf("zero spec class = %v", spec.DataClass)
	}
}
