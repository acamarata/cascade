//go:build windows

// Purpose: the windows half of the local CI gate's timeout
// (P1-E25-W5-S51-T5), and an honest statement of what it cannot do.
//
// WHICH PLATFORM CANNOT REAP THE TREE: THIS ONE. This is a real, complete
// implementation of what Windows offers, plus a typed refusal for the part
// it does not -- not a placeholder. A step is started with
// CREATE_NEW_PROCESS_GROUP, so the shell is its own group leader and the
// deadline reliably kills the shell. There is no portable equivalent of
// unix's kill(-pgid) here: reaping a whole tree on Windows means either a
// Job Object (CreateJobObject/AssignProcessToJobObject/
// TerminateJobObject, none of which this module's dependency set exposes)
// or shelling out to taskkill /T, which would add a second uncontrolled
// sub-process to the very code path that just failed to bound the first.
// Both are deliberate rejections with stated reasons, and a later ticket
// that adds a Job Object binding replaces the refusal below with a reap.
// Rather than pretend, killProcessGroup returns a TYPED refusal naming the
// platform and what was and was not reaped; runner_exec.go records it on
// ExecResult.GroupKillErr, the step is still reported as timed out, and
// cmd.WaitDelay still guarantees Run returns instead of hanging on a pipe a
// survivor holds. An operator on
// Windows therefore learns, in the result itself, that a grandchild may
// have outlived the step -- which is a disclosed limitation, not a silent
// one.
//
// Inputs/Outputs/Constraints: as runner_exec_unix.go, minus the group
// reap.
// SPORT: internal.ci.killProcessGroup/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"os/exec"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP. Named rather than
// inlined so the flag's meaning is readable without a Win32 reference.
const createNewProcessGroup = 0x00000200

// setProcessGroup starts the command as its own process-group leader.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// killProcessGroup kills the step's shell and refuses, typed, to claim it
// reaped the tree. See this file's doc comment for why.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	killErr := cmd.Process.Kill()
	refusal := cascade.New(cascade.KindUnsupported,
		"ci: this step timed out and its shell was killed, but windows offers no portable whole-process-tree reap here; a backgrounded grandchild may still be running")
	if killErr != nil {
		return cascade.Wrap(cascade.KindUnavailable, killErr, "ci: killing the timed-out step")
	}
	return refusal
}
