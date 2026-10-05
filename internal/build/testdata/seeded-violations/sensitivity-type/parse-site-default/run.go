// Seeded violation for internal/build/arch_sensitivity_test.go (P1-SEC-19).
// It lives under testdata/ so the toolchain never compiles it. Do not "fix" it.
package main

import "github.com/acamarata/cascade/pkg/provider"

// validateSensitivity keeps a local default for "" beside the closed
// parser, which the source-shape assertion must catch.
func validateSensitivity(tier string) error {
	if tier == "" {
		return nil
	}
	_, err := provider.ParseSensitivityTier(tier)
	return err
}
