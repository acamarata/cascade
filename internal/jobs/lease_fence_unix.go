//go:build !windows

package jobs

// Purpose: unixLivenessProbe, the production ProcessLivenessProbe for
//
//	non-Windows platforms -- a signal-0 probe, the standard liveness
//	check that delivers no actual signal but still performs the kernel's
//	existence/permission check. Mirrors internal/daemon's unixProber
//	(lifecycle_unix_prober.go) closely; not reused directly because
//	internal/jobs importing internal/daemon would invert this repo's
//	layering (daemon composes jobs, not the reverse), so this ticket
//	ships its own minimal copy of the one probe call it needs.
//
// Inputs: a pgid (process GROUP id -- negating it targets the whole
//
//	group via kill(2)'s documented convention, so a probe against a
//	pgid, not a bare pid, reaches the right target even if the leader
//	itself has already exited but a child remains).
//
// Outputs: IsAlive's bool.
// Constraints: fail-closed. false only on ESRCH for pgid 1, or on a
//
//	failed group signal for a pgid above 1 (a group this daemon cannot
//	signal is not one it spawned); every other outcome is alive. kill(2) reads pid 0 as the
//	caller's own group and -1 as every process, and a pgid above the
//	32-bit pid_t range truncates to some other target, so a pgid <= 0
//	or above math.MaxInt32 is never probed and reads as alive. pgid 1
//	probes pid 1 itself (never kill(-1, 0)): only ESRCH is dead. The
//	probe never calls kill with pid -1 or 0. livenessKill is the only
//	kill call, an unexported seam tests swap for a recording fake.
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"errors"
	"math"
	"syscall"
)

// unixLivenessProbe is the zero-value-usable production
// ProcessLivenessProbe for this platform.
type unixLivenessProbe struct{}

// livenessKill is the kill(2) call IsAlive makes, as a package var so
// tests can record (pid, sig) and inject an errno without sending a real
// signal. Production never reassigns it.
var livenessKill = syscall.Kill

// NewProcessLivenessProbe returns the production ProcessLivenessProbe
// for this platform, for the daemon composition root to inject.
func NewProcessLivenessProbe() ProcessLivenessProbe { return unixLivenessProbe{} }

// IsAlive reports whether the process group pgid may still be live;
// false means confirmed dead, and callers fence or sweep only on false.
//
//   - pgid <= 0 or pgid > math.MaxInt32: true, no kill call. kill(2)
//     reads 0 as the caller's own group and -1 as every process, and an
//     out-of-range pgid truncates to another target, so the probe cannot
//     confirm death, and unknown must never read as dead.
//   - pgid == 1: signal 0 to pid 1 itself. Only ESRCH is dead; nil and
//     EPERM are alive, and any other errno is unknown, so alive.
//   - pgid > 1: signal 0 to the group -pgid; alive only on nil.
func (unixLivenessProbe) IsAlive(pgid int64) bool {
	if pgid <= 0 || pgid > math.MaxInt32 {
		return true
	}
	if pgid == 1 {
		return !errors.Is(livenessKill(1, syscall.Signal(0)), syscall.ESRCH)
	}
	return livenessKill(-int(pgid), syscall.Signal(0)) == nil
}
