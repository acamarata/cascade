//go:build !windows

// Purpose (this file): the unix half of the detection probe's timeout —
//
//	start the child in its own process group and, on the deadline, signal
//	the GROUP, so a grandchild the CLI backgrounded cannot outlive the
//	probe that spawned it. internal/ci's runner_exec_unix.go precedent,
//	re-stated here because plugins/** may not import internal/**
//	(Art.10.2).
//
// Inputs: the *exec.Cmd about to be started, then the same Cmd running.
// Outputs: nothing on the start side; a typed error on the kill side only
//
//	when the group could not be signalled for a reason other than "it is
//	already gone".
//
// Constraints: Setpgid makes the child its own group leader, so its pgid
//
//	equals its pid and Kill(-pid) reaches the whole tree — which is why
//	the kill uses the negative pid rather than cmd.Process.Kill.
//
// SPORT: plugins/nself detect (ADD) — P1-E25-W5-S52-T2.

package nself

import (
	"errors"
	"os/exec"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// setProcessGroup puts the command in a new process group of its own.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup signals the command's whole process group. SIGKILL
// rather than SIGTERM: this runs only after the probe already blew its 2s
// bound. ESRCH means the group exited between the deadline and the signal,
// which is success, not a failure to report.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"nself: killing the probe's process group (pgid %d)", cmd.Process.Pid)
	}
	return nil
}
