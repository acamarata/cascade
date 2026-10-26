//go:build darwin

package agents

// Purpose: the darwin peer inspector: environment from kern.procargs2, parent
// from the kern.proc.pid record, process group and session from getpgid and
// getsid.
//
// Constraints: a denied or vanished process is an error, never "no entries"
// (the guard refuses it). The kernel withholds the environment of Apple-signed
// system binaries; for those the read succeeds without it and the marker check
// sees nothing, which is the disclosed darwin residual (the process group and
// ancestor checks still apply).

import (
	"golang.org/x/sys/unix"
)

// sysctlInspector reads the BSD process table.
type sysctlInspector struct{}

// platformPeerInspector is the sysctl inspector.
func platformPeerInspector() (peerInspector, error) { return sysctlInspector{}, nil }

// holds reads kern.procargs2 for pid.
func (sysctlInspector) holds(pid int, entries []string) (bool, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if procArgsHas(buf, e) {
			return true, nil
		}
	}
	return false, nil
}

// groups returns pid's process group and session.
func (sysctlInspector) groups(pid int) (int, int, error) {
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		return 0, 0, err
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return 0, 0, err
	}
	return pgid, sid, nil
}

// parent returns pid's parent from its kinfo_proc record.
func (sysctlInspector) parent(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, unix.ESRCH
	}
	return int(kp.Eproc.Ppid), nil
}
