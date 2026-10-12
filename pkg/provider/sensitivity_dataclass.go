package provider

import "github.com/acamarata/cascade/pkg/cascade"

// Purpose: preserve stored data-class words while sharing the canonical tier.
// Inputs: a tier, a stored word, or an optional evaluation-time tier.
// Outputs: validated stored text or a fail-closed tier.
// Constraints: this table is the sole stored vocabulary; nil is unset.
// SPORT: sensitivity stored vocabulary.
var dataClassNames = [...]string{"confidential", "secret", "internal", "public"}

// DataClassName returns the stable stored name of a valid tier.
func DataClassName(t SensitivityTier) (string, error) {
	if !t.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput, "provider: unknown data_class tier %d", t)
	}
	return dataClassNames[t], nil
}

// ParseDataClassName refuses words outside the exact stored vocabulary.
func ParseDataClassName(s string) (SensitivityTier, error) {
	for i, name := range dataClassNames {
		if s == name {
			return SensitivityTier(i), nil
		}
	}
	return SensitivityLocalOnly, cascade.Newf(cascade.KindInvalidInput, "provider: unknown data_class %q", s)
}

// TierOrLocalOnly resolves unset and invalid evaluation inputs conservatively.
func TierOrLocalOnly(p *SensitivityTier) SensitivityTier {
	if p == nil || !p.Valid() {
		return SensitivityLocalOnly
	}
	return *p
}
