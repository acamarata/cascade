//go:build !windows

// Purpose: the unix half of the local CI gate's timeout (P1-E25-W5-S51-T5):
// start each step in its own process group, and on a deadline signal the
// GROUP so a backgrounded grandchild cannot outlive the step that spawned
// it. See runner_exec.go's "WHY THE TIMEOUT KILLS A GROUP" note for what
// goes wrong without this.
//
// Inputs: the *exec.Cmd about to be started, then the same Cmd once
// running.
// Outputs: nothing on the start side; a typed error on the kill side only
// when the group could not be signalled for a reason other than "it is
// already gone".
// Constraints: Setpgid makes the child its own group leader, so its pgid
// equals its pid and syscall.Kill(-pid) reaches the whole tree. This is
// why the kill uses the negative pid rather than cmd.Process.Kill.
// SPORT: internal.ci.killProcessGroup/ADDED (P1-E25-W5-S51-T5).

package ci

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

// killProcessGroup signals the command's whole process group.
//
// SIGKILL rather than SIGTERM: this runs only after a step already blew
// its configured timeout, and a build tool that ignores TERM (or a shell
// that forwards it nowhere) would leave the same orphan the group kill
// exists to prevent. ESRCH means the group exited between the deadline and
// the signal, which is success, not a failure to report.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"ci: killing the step's process group (pgid %d)", cmd.Process.Pid)
	}
	return nil
}
