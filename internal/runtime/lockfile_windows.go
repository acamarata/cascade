//go:build windows

// Purpose: windows stub for ProcessAlive. The daemon itself is
//   tier-2-unsupported on Windows (D/S-07.T4) and recovery.go's socket
//   probe returns cleanly before ever reaching a pidfile or lock check on
//   this platform (HOW step 1 of P1-E03-W1-S05-T3's contract), so this
//   function is never called in production on windows. It exists only so
//   internal/runtime compiles under GOOS=windows (the
//   "GOOS=windows GOARCH=amd64 go build ./internal/runtime/..." check) —
//   split from lockfile.go under R-14.133, same footing as
//   lockfile_unix.go.
// SPORT: runtime/lockfile (ADD).

package runtime

import "errors"

// errProcessAliveUnsupportedWindows documents why this stub returns
// Undecided unconditionally: there is no POSIX kill(pid, 0) equivalent
// wired here, and recovery.go's windows path never calls this function,
// so fabricating a Windows liveness probe would be untested, unreachable
// code — exactly what Art.1 forbids.
var errProcessAliveUnsupportedWindows = errors.New("lockfile: ProcessAlive is unsupported on windows (daemon is tier-2 unsupported; recovery.go never calls this on windows)")

// ProcessAlive always reports ProcessLivenessUndecided on windows. See
// the package doc above for why this is correct rather than a stub that
// pretends to a real answer.
func ProcessAlive(pid int) (ProcessLiveness, error) {
	return ProcessLivenessUndecided, errProcessAliveUnsupportedWindows
}
