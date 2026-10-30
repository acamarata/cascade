//go:build windows

// Purpose (this file): ComputeToolchainFingerprint on Windows (P1-CI-03).
// Windows is a tier-2 platform for CI execution: there is no uname and the
// clean-room executor is not supported, so the fingerprint is refused and
// a toolchain-bound sub-job ends as an explicit non-pass.
//
// Inputs: none.
// Outputs: ErrPlatformUnsupported, always.
// Constraints: exactly one ComputeToolchainFingerprint definition per GOOS.
// SPORT: internal.ci.ComputeToolchainFingerprint/ADDED (P1-CI-03).

package ci

import "context"

// ComputeToolchainFingerprint refuses on Windows with ErrPlatformUnsupported.
func ComputeToolchainFingerprint(context.Context) (string, error) {
	return "", ErrPlatformUnsupported
}
