//go:build darwin || linux

// Purpose: darwin/linux implementation of ProcessAlive — the POSIX
//   kill(pid, 0) liveness probe. Split from lockfile.go under R-14.133 (a
//   `//go:build` file split joins the ticket's authorized write set
//   automatically, same footing as that ruling's `//go:build integration`
//   case): golang.org/x/sys/unix is a unix-only import and lockfile.go
//   itself must stay buildable under GOOS=windows (the
//   "GOOS=windows GOARCH=amd64 go build ./internal/runtime/..." check),
//   so the syscall-touching half cannot live in the shared file. Uses the
//   explicit `darwin || linux` constraint rather than the Go 1.19+ `unix`
//   build-tag shorthand: internal/build's desktop-only-import gate
//   (P1-E01-W1-S01-T2) recognizes a build tag as a platform constraint
//   only when it names one of its own desktopGOOS literals
//   (darwin/linux/windows/freebsd/js/wasm/android/ios) — "unix" is not
//   among them, so a `//go:build unix` file trips
//   TestNoDesktopOnlyImports_RealTreeGreen as an "unconstrained
//   platform-only import" even though it plainly is constrained. Explicit
//   GOOS naming also matches this ticket's actual release-platform scope
//   (darwin+linux; freebsd/openbsd are not release platforms).
// Constraints: golang.org/x/sys/unix is an existing go.mod dependency
//   (already used by providers/sqlite/flock_linux.go) — no new
//   dependency is introduced.
// SPORT: runtime/lockfile (ADD).

package runtime

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// ProcessAlive sends signal 0 to pid — POSIX's standard "does this
// process exist" probe: the kernel performs its full existence and
// permission check without ever delivering a signal.
//
//   - ESRCH ("no such process"): the kernel has no live process at this
//     pid — ProcessLivenessDead, the only result recovery.go treats as
//     license to remove an artifact naming this pid.
//   - EPERM ("operation not permitted"): a process DOES exist at this pid
//     (the kernel found a live slot to permission-check against) but this
//     daemon lacks rights to signal it — ProcessLivenessAlive. Reported
//     as Alive, not Dead: "exists but I can't prove it's mine" must never
//     be treated as safe to clean up.
//   - nil: pid belongs to this process's own user — ProcessLivenessAlive.
//   - any other error (no POSIX kill(2) errno besides ESRCH/EPERM/EINVAL
//     is defined for signal 0, and EINVAL cannot occur for a real signal
//     value of 0): ProcessLivenessUndecided, so an unexpected OS failure
//     defaults to "do not delete" rather than being silently folded into
//     either decided outcome.
func ProcessAlive(pid int) (ProcessLiveness, error) {
	if pid <= 0 {
		return ProcessLivenessUndecided, fmt.Errorf("lockfile: invalid pid %d", pid)
	}
	err := unix.Kill(pid, 0)
	switch {
	case err == nil:
		return ProcessLivenessAlive, nil
	case errors.Is(err, unix.ESRCH):
		return ProcessLivenessDead, nil
	case errors.Is(err, unix.EPERM):
		return ProcessLivenessAlive, nil
	default:
		return ProcessLivenessUndecided, fmt.Errorf("lockfile: kill(%d, 0) probe: %w", pid, err)
	}
}
