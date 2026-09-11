//go:build !windows

package jobs

// Purpose: unixLivenessProbe, the production ProcessLivenessProbe for
//
//	non-Windows platforms -- a signal-0 probe, the standard liveness
//	check that delivers no actual signal but still performs the kernel's
//	existence/permission check. Mirrors internal/daemon's unixProber
//	(lifecycle_unix_prober.go) exactly; not reused directly because
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
// Constraints: pgid <= 0 is never alive by construction (Reclaim never
//
//	calls this probe for a pgid it did not already confirm > 0, but the
//	guard is defensive since os.FindProcess(0) has platform-specific
//	behavior this file must not depend on).
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"os"
	"syscall"
)

// unixLivenessProbe is the zero-value-usable production
// ProcessLivenessProbe for this platform.
type unixLivenessProbe struct{}

// NewProcessLivenessProbe returns the production ProcessLivenessProbe
// for this platform, for the daemon composition root to inject.
func NewProcessLivenessProbe() ProcessLivenessProbe { return unixLivenessProbe{} }

// IsAlive sends signal 0 to the process GROUP -pgid: a nil error means a
// live process group this caller has rights to signal.
func (unixLivenessProbe) IsAlive(pgid int64) bool {
	if pgid <= 0 {
		return false
	}
	proc, err := os.FindProcess(int(-pgid))
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
