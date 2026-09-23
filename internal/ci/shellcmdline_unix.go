//go:build !windows

// Purpose: the non-windows half of the shellcmdline build-tag pair (see
// shellcmdline_windows.go's header for the windows-only bug this exists
// to fix and why a build-tag pair rather than a runtime.GOOS branch --
// same precedent as runner_exec_unix.go/runner_exec_windows.go and
// waitmerge_unix.go/waitmerge_windows.go in this package).
//
// WHY THIS IS A NO-OP HERE: unix's exec.Cmd builds argv directly from
// Args -- one element per fork/exec argv slot, no re-escaping into a
// single string -- so `sh -c "<command>"` already receives the operator's
// command byte-for-byte. There is no analogous re-quoting bug to work
// around, and syscall.SysProcAttr on unix carries no CmdLine field to set.
// SPORT: internal.ci.setShellCmdLine/ADDED.

package ci

import "os/exec"

// setShellCmdLine is a no-op outside windows.
func setShellCmdLine(cmd *exec.Cmd, command string) {
	_ = cmd
	_ = command
}
