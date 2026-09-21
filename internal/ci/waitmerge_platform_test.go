// Purpose: one helper for every WaitOnGreen / MergeOnGreen test that
// exercises the loop PAST the platform gate. On Windows (tier-2) the gate
// refuses before any poll, so those tests would fail for the gate's reason
// rather than their own; they skip with that reason instead, while the
// gate itself stays asserted natively by waitmerge_windows_test.go and
// waitmerge_unix_test.go. A skip here is never a pass: the windows lane
// reports each one by name.
//
// SPORT: internal.ci.WaitOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import "testing"

// skipWhenWaitIsRefusedHere skips the calling test when this platform's
// half of the gate refuses wait-on-green outright.
func skipWhenWaitIsRefusedHere(t *testing.T) {
	t.Helper()
	if err := waitDaemonAbsentRefusal(); err != nil {
		t.Skipf("wait-on-green is refused on this platform before any poll: %v", err)
	}
}
