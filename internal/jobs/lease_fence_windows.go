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
// Constraints: fail-closed. IsAlive reports dead only for a proven
//
//	absent pid (OpenProcess fails with ERROR_INVALID_PARAMETER) or a
//	real exit code (GetExitCodeProcess succeeds with a code other than
//	STILL_ACTIVE). ERROR_ACCESS_DENIED on a protected or other-session
//	process, and every other OpenProcess or GetExitCodeProcess error,
//	reads alive. The classification lives in winHandleVerdict
//	(lease_fence_winverdict.go); this file holds no errno or exit-code
//	comparison. Two known pins read alive, never dead: a holder that
//	exited with code 259, and a pid Windows reused for another process.
//	A pgid <= 0 or above math.MaxUint32 cannot name a Windows process id
//	(it would truncate to another pid), so the probe reports alive
//	without calling any seam. Every handle OpenProcess returns is closed
//	exactly once. The three Windows calls are unexported package vars
//	tests swap for a recording fake, so no test touches a real handle.
//
// SPORT: jobs/lease-model (ADD, P1-E29-W6-S59-T2).

import (
	"math"

	"golang.org/x/sys/windows"
)

// livenessOpenProcess, livenessGetExitCodeProcess and
// livenessCloseHandle are the Windows calls IsAlive makes, as package
// vars so tests can record their arguments and inject results without
// opening, querying or closing a real handle. Production never
// reassigns them.
var (
	livenessOpenProcess        = windows.OpenProcess
	livenessGetExitCodeProcess = windows.GetExitCodeProcess
	livenessCloseHandle        = windows.CloseHandle
)

// windowsLivenessProbe is the zero-value-usable production
// ProcessLivenessProbe for this platform.
type windowsLivenessProbe struct{}

// NewProcessLivenessProbe returns the production ProcessLivenessProbe
// for this platform, for the daemon composition root to inject.
func NewProcessLivenessProbe() ProcessLivenessProbe { return windowsLivenessProbe{} }

// IsAlive opens pgid (as a Windows pid) with query-limited rights and
// reads its exit code, then lets winHandleVerdict decide. It is
// fail-closed: dead only when OpenProcess fails with
// ERROR_INVALID_PARAMETER or GetExitCodeProcess succeeds with a code
// other than STILL_ACTIVE; every other error reads alive. A holder that
// exited with code 259 and a reused pid both read alive, never dead. A
// pgid <= 0 or above math.MaxUint32 is not a pid this probe can address,
// so it returns true without calling any seam. An opened handle is
// closed exactly once.
func (windowsLivenessProbe) IsAlive(pgid int64) bool {
	if pgid <= 0 || pgid > math.MaxUint32 {
		return true
	}
	h, err := livenessOpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pgid))
	if err != nil {
		return winHandleVerdict(err, nil, 0)
	}
	defer func() { _ = livenessCloseHandle(h) }()
	var code uint32
	exitErr := livenessGetExitCodeProcess(h, &code)
	return winHandleVerdict(nil, exitErr, code)
}
