//go:build !windows

// Purpose: the non-windows half of wait-on-green's platform gate, asserted
// natively on every lane that is not windows/amd64 -- the build-tag twin of
// waitmerge_windows_test.go, so neither half is ever asserted by a
// runtime.GOOS branch.
//
// SPORT: internal.ci.waitDaemonAbsentRefusal/TEST (P1-E25-W5-S51-T3).
package ci

import "testing"

func TestWaitDaemonAbsentRefusal_NonWindowsIsNil(t *testing.T) {
	if err := waitDaemonAbsentRefusal(); err != nil {
		t.Fatalf("waitDaemonAbsentRefusal() = %v, want nil on this platform", err)
	}
}
