//go:build !windows

// Purpose: the unix processTree. Unix needs no job: the step already runs
// in its own process group (runner_exec_unix.go), a kill reaches the whole
// group, and a unix directory can be removed while a process still uses
// it. So the tree is empty and kill is the existing group kill, unchanged.
//
// Inputs: the step's *exec.Cmd.
// Outputs: killProcessGroup's error on kill; nil otherwise.
// Constraints: no behaviour change on darwin or linux.
// SPORT: internal.ci.processTree/ADDED.

package ci

import "os/exec"

// processTree is empty on unix; see this file's header.
type processTree struct{}

// newProcessTree never fails on unix.
func newProcessTree() (processTree, error) { return processTree{}, nil }

// attach is a no-op: setProcessGroup already confined the step.
func (processTree) attach(*exec.Cmd) error { return nil }

// kill signals the step's whole process group.
func (processTree) kill(cmd *exec.Cmd) error { return killProcessGroup(cmd) }

// close is a no-op on unix.
func (processTree) close() {}
