//go:build windows

package ci

// Tests for ComputeToolchainFingerprint on Windows (P1-CI-03).

import (
	"context"
	"testing"
)

// TestToolchainFingerprintWindowsRefuses: Windows is tier 2, the
// fingerprint is refused with ErrPlatformUnsupported.
func TestToolchainFingerprintWindowsRefuses(t *testing.T) {
	fp, err := ComputeToolchainFingerprint(context.Background())
	if fp != "" || !errChainHas(err, ErrPlatformUnsupported) {
		t.Fatalf("ComputeToolchainFingerprint = %q, %v; want ErrPlatformUnsupported", fp, err)
	}
	t.Logf("refused as required: %v", err)
}
