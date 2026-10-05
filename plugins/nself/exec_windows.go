//go:build windows

// Purpose (this file): the windows half of the detection probe's timeout,
//
//	and an honest statement of what it cannot do.
//
// WHICH PLATFORM CANNOT REAP THE TREE: THIS ONE. The child is started with
//
//	CREATE_NEW_PROCESS_GROUP, so it is its own group leader and the
//	deadline reliably kills it. There is no portable equivalent of unix's
//	kill(-pgid) here: reaping a whole tree on Windows means a Job Object
//	(CreateJobObject/AssignProcessToJobObject/TerminateJobObject, none of
//	which this module's dependency set exposes) or shelling out to
//	`taskkill /T`, which would add a second unbounded sub-process to the
//	code path that just failed to bound the first. Both are deliberate
//	rejections with stated reasons — internal/ci's runner_exec_windows.go
//	reached the same conclusion for the same platform. So the refusal is
//	TYPED and surfaced rather than pretended away; cmd.WaitDelay still
//	guarantees the probe returns.
//
// Inputs/Outputs/Constraints: as exec_unix.go, minus the group reap. The
//
//	reaped guard is the same: a command already waited for is never
//	killed (R134), and deliverGroupKill is the same test seam.
//
// SPORT: plugins/nself detect (ADD) — P1-E25-W5-S52-T2; kill seam (CHANGE) — P1-PLG-09.

package nself

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP, named rather than
// inlined so the flag is readable without a Win32 reference.
const createNewProcessGroup = 0x00000200

// deliverGroupKill sends the kill once killProcessGroup has decided one is
// due: on windows the child alone, since there is no group signal. It is a
// variable only so a test can prove no kill is sent for a reaped command;
// production code never reassigns it.
var deliverGroupKill = func(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// leaderWaited reports whether p was already waited for, asking p itself
// rather than racing Wait's write of cmd.ProcessState (see exec_unix.go).
func leaderWaited(p *os.Process) bool {
	return errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

// setProcessGroup starts the command as its own process-group leader.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// killProcessGroup kills the probe and refuses, typed, to claim it reaped
// the tree. See this file's doc comment for why.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.ProcessState != nil || leaderWaited(cmd.Process) {
		// Never started, or already waited for: there is nothing left to
		// kill, which is the same success unix reports as ESRCH.
		return nil
	}
	if err := deliverGroupKill(cmd); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "nself: killing the timed-out probe")
	}
	return cascade.New(cascade.KindUnsupported,
		"nself: the probe timed out and was killed, but windows offers no portable whole-process-tree reap here; a backgrounded grandchild may still be running")
}
