package agents

// Purpose: the darwin process source: pids from the kern.proc.uid sysctl,
// environments from kern.procargs2 (KERN_PROCARGS2).
//
// Constraints: only this uid's processes are listed. A zombie or a process
// whose arguments the kernel will not return has no entries.

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// statStopped is the BSD p_stat value of a stopped process (SSTOP).
const statStopped = 4

// sysctlSource reads the BSD process table.
type sysctlSource struct{}

// platformSource is the sysctl source.
func platformSource() (procSource, error) { return sysctlSource{}, nil }

// list returns the pids whose effective uid is ours.
func (sysctlSource) list() ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(procs))
	for _, p := range procs {
		if p.Proc.P_pid > 0 {
			out = append(out, int(p.Proc.P_pid))
		}
	}
	return out, nil
}

// hasEntry reads kern.procargs2 for pid.
func (sysctlSource) hasEntry(pid int, entry string) (bool, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if noEntries(err) {
			return false, nil
		}
		return false, err
	}
	return procArgsHas(buf, entry), nil
}

// kill stops the pid, re-checks the entry on the stopped process and only then
// kills it; anything that no longer carries the entry is resumed, unless it
// was already stopped before we touched it. Darwin has no pidfd, so a process
// cannot be pinned: the stop keeps the target from exiting on its own, which
// leaves only another process killing and reaping it inside one sysctl while a
// new process takes the pid, and the owner is checked before the stop so
// another uid's process is never signalled.
func (s sysctlSource) kill(pid int, entry string) error {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if noEntries(err) {
			return nil
		}
		return err
	}
	if kp.Eproc.Ucred.Uid != uint32(os.Geteuid()) {
		return nil
	}
	wasStopped := kp.Proc.P_stat == statStopped
	if !wasStopped {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			if err == syscall.ESRCH {
				return nil
			}
			return err
		}
	}
	has, err := s.hasEntry(pid, entry)
	sig := syscall.SIGKILL
	if err != nil || !has {
		if wasStopped {
			return err
		}
		sig = syscall.SIGCONT
	}
	if kerr := syscall.Kill(pid, sig); kerr != nil && kerr != syscall.ESRCH {
		return kerr
	}
	return err
}
