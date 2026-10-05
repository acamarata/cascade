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
//	equals its pid and a signal to that group reaches the whole tree —
//	which is why the kill goes through pkg/procgroup rather than
//	cmd.Process.Kill. A command whose leader was already waited for is
//	never signalled: its pid, and so its pgid, can be reused by an
//	unrelated group (R134; EPERM under whole-tree load).
//
// SPORT: plugins/nself detect (ADD) — P1-E25-W5-S52-T2; procgroup caller + reaped guard (CHANGE) — P1-PLG-09.

package nself

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
)

// deliverGroupKill sends the kill once killProcessGroup has decided one is
// due. It is a variable only so a test can prove no signal is sent for a
// reaped command; production code never reassigns it.
var deliverGroupKill = func(cmd *exec.Cmd) error {
	return procgroup.Signal(cmd.Process.Pid, syscall.SIGKILL)
}

// leaderWaited reports whether p was already waited for. It asks p itself
// rather than reading cmd.ProcessState: killProcessGroup runs as cmd.Cancel
// on os/exec's context goroutine while Wait writes ProcessState, so that
// read is a data race. os.Process marks itself done before it reaps, and
// its Signal reports ErrProcessDone from then on, with no race.
func leaderWaited(p *os.Process) bool {
	return errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

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
// which is success, not a failure to report. A leader already waited for
// is never signalled (see this file's Constraints).
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || leaderWaited(cmd.Process) {
		return nil
	}
	if err := deliverGroupKill(cmd); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"nself: killing the probe's process group (pgid %d)", cmd.Process.Pid)
	}
	return nil
}
