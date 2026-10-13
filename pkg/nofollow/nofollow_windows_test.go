//go:build windows

// Purpose: prove the windows flags are 0, as the package doc states.
//
// SPORT: pkg/nofollow (TEST) — P1-AGT-01.

package nofollow

import "testing"

func TestFlagsAreZeroOnWindows(t *testing.T) {
	if NoFollow != 0 || NonBlock != 0 {
		t.Fatalf("NoFollow %d NonBlock %d, want 0 0", NoFollow, NonBlock)
	}
}
