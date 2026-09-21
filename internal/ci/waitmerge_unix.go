//go:build !windows

// Purpose: the non-Windows half of wait-on-green's platform gate.
//
// Constraints: this file and waitmerge_windows.go are a BUILD-TAG PAIR
// (not a runtime.GOOS branch), matching plugins/cascade-pa/telegram's
// module_unix.go/module_windows.go precedent: a GOOS branch is unreachable
// on every CI lane but one, and the windows/amd64 lane must compile the
// refusal it is meant to prove (R-16.47, R-16.60a).
//
// SPORT: internal.ci.waitDaemonAbsentRefusal/ADDED (P1-E25-W5-S51-T3).

package ci

// waitDaemonAbsentRefusal reports no refusal on this platform: a future
// `cascade github ci wait` entrypoint may proceed here.
func waitDaemonAbsentRefusal() error { return nil }
