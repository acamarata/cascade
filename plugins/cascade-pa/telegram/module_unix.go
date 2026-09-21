//go:build !windows

// Purpose: the non-Windows half of the bridge's platform gate — there is a
//   daemon here, so nothing is refused.
//
// Constraints: this file and module_windows.go are a BUILD-TAG PAIR rather
//   than one runtime.GOOS branch, because a GOOS branch is unreachable code
//   on every lane but one: the windows/amd64 CI lane can only execute the
//   refusal if the refusal is what that lane COMPILES (R-16.47, R-16.60a).
//
// SPORT: plugins/cascade-pa/telegram platformBridgeRefusal/ADDED
//   (P1-E23-W5-S48-T1).

package telegram

// platformBridgeRefusal reports no refusal: this platform runs the daemon.
func platformBridgeRefusal() error { return nil }
