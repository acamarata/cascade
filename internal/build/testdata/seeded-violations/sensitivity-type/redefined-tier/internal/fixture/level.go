// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package fixture

import "github.com/acamarata/cascade/pkg/provider"

// Level is a defined type over the canonical tier (not an alias): a second
// declaration with its own method set, so the gate fires.
type Level provider.SensitivityTier
