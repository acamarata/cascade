// Purpose: the platform-dependent expectation for the inbox push deferral. On
// Windows the channel returns its tier-2 refusal before any probe result
// matters; elsewhere the flag-off deferral is the unsupported-channel one.
// SPORT: plugins/claude (P1-HRN-02).
package claude

import (
	"runtime"
	"testing"
)

// wantPlatformDeferral asserts the deferral the running platform produces.
func wantPlatformDeferral(t *testing.T, got error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		wantExactly(t, got, ErrWindowsUnsupported, "tier-2")
		return
	}
	wantExactly(t, got, ErrChannelUnsupported, "D-S73T1-1")
}
