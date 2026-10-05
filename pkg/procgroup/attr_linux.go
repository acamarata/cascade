//go:build linux

// Purpose: the linux SysProcAttr: a new process group, plus a parent-death
//
//	signal so a child does not outlive a daemon that was SIGKILLed.
//
// Inputs: none.
// Outputs: a fresh *syscall.SysProcAttr per call.
// Constraints: Pdeathsig fires when the thread that forked the child
//
//	exits, not the whole process; the Go runtime keeps that thread alive
//	for the life of an ordinary program, so it is the backstop for a
//	SIGKILLed daemon, never the primary kill path.
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09.

package procgroup

import "syscall"

// Attr returns the SysProcAttr that makes a started child the leader of a
// new process group (pgid == pid) and SIGKILLs it if its parent dies.
func Attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
