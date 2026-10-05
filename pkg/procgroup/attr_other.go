//go:build !linux && !windows

// Purpose: the SysProcAttr for darwin and the other unix platforms: a new
//
//	process group only.
//
// Inputs: none.
// Outputs: a fresh *syscall.SysProcAttr per call.
// Constraints: these platforms have no parent-death signal, so a child of
//
//	a SIGKILLed daemon runs until it reads EOF on its stdin. That residual
//	is recorded against P1-PLG-09, not hidden here.
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09.

package procgroup

import "syscall"

// Attr returns the SysProcAttr that makes a started child the leader of a
// new process group (pgid == pid).
func Attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
