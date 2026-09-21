// Purpose: one helper for every cascade github ci watch test that crosses
// the daemon-owned attention path. On Windows (tier-2) the platform gate
// refuses that path before any store opens, so those tests would fail for
// the gate's reason rather than their own; they skip with that reason
// instead, while the gate itself stays asserted natively by internal/ci's
// build-tag pair. A skip here is never a pass: the windows lane reports
// each one by name.
//
// SPORT: cmd.cascade.githubCIWatch/TESTED (P1-E25-W5-S51-T4).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/ci"
)

// skipWhenTheDaemonIsRefusedHere skips the calling test when this
// platform's half of the gate refuses the daemon-owned attention path.
func skipWhenTheDaemonIsRefusedHere(t *testing.T) {
	t.Helper()
	if err := ci.PlatformRefusal(); err != nil {
		t.Skipf("daemon-owned attention path is refused on this platform: %v", err)
	}
}
