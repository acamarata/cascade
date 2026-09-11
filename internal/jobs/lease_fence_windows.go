//go:build windows

package jobs

// Purpose: windowsLivenessProbe, the production ProcessLivenessProbe for
//
//	Windows: the "equivalent handle probe" R-21.177 asks for in place of
//	the unix signal-0 check. Windows has no POSIX process-group signal;
//	OpenProcess + GetExitCodeProcess against the recorded pgid (treated
//	as this platform's process id -- there is no separate group id to
//	record on Windows, a documented platform limitation, not a bug)
//	tells us whether that process handle still reports STILL_ACTIVE.
//
// Inputs: a pgid, read as a Windows process id on this platform.
// Outputs: IsAlive's bool.
// Constraints: never dereferences a zero/invalid handle; OpenProcess
//
//	failing (already exited, or never existed) means "not alive", never
//	a panic.
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import "golang.org/x/sys/windows"

// windowsLivenessProbe is the zero-value-usable production
// ProcessLivenessProbe for this platform.
type windowsLivenessProbe struct{}

// NewProcessLivenessProbe returns the production ProcessLivenessProbe
// for this platform, for the daemon composition root to inject.
func NewProcessLivenessProbe() ProcessLivenessProbe { return windowsLivenessProbe{} }

// IsAlive opens pgid (as a Windows pid) with query-limited rights and
// checks GetExitCodeProcess for STILL_ACTIVE.
func (windowsLivenessProbe) IsAlive(pgid int64) bool {
	if pgid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pgid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == uint32(windows.STATUS_PENDING) // STILL_ACTIVE has the same numeric value (259)
}
