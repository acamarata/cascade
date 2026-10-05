package jobs

// Purpose: winHandleVerdict, the pure classification behind the Windows
//
//	handle liveness probe. It lives in an untagged file so every OS
//	tests and mutates it; lease_fence_windows.go only calls the
//	OpenProcess/GetExitCodeProcess seams and hands their results here.
//
// Inputs: OpenProcess's error, GetExitCodeProcess's error, and the exit
//
//	code GetExitCodeProcess wrote.
//
// Outputs: true (alive, never fence) or false (confirmed dead).
// Constraints: fail-closed. Only two outcomes prove death: OpenProcess
//
//	failing with ERROR_INVALID_PARAMETER (no such process), or
//	GetExitCodeProcess succeeding with a code other than STILL_ACTIVE.
//	ERROR_ACCESS_DENIED (a protected or other-session process, which is
//	alive) and every other error read as alive.
//
// SPORT: jobs/lease-model.

import (
	"errors"
	"syscall"
)

// winErrorInvalidParameter is Windows' ERROR_INVALID_PARAMETER (87),
// the errno OpenProcess returns for a process id that does not exist.
// x/sys/windows declares it as this same syscall.Errno value.
const winErrorInvalidParameter = syscall.Errno(87)

// winStillActive is Windows' STILL_ACTIVE (259), the exit code
// GetExitCodeProcess reports for a process that has not exited.
const winStillActive = 259

// winHandleVerdict classifies one Windows handle probe. A non-nil
// openErr is dead only when it is (or wraps) ERROR_INVALID_PARAMETER; a
// non-nil exitErr is always alive; otherwise the process is alive
// exactly when code is STILL_ACTIVE.
func winHandleVerdict(openErr, exitErr error, code uint32) bool {
	if openErr != nil {
		return !errors.Is(openErr, winErrorInvalidParameter)
	}
	if exitErr != nil {
		return true
	}
	return code == winStillActive
}
