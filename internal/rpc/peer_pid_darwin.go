//go:build darwin

package rpc

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPIDFromConn resolves the pid of the process that connected conn via
// LOCAL_PEERPID (darwin). conn must be a *net.UnixConn; any other type, a
// syscall failure or a non-positive pid reports ok=false, fail-closed.
func peerPIDFromConn(conn net.Conn) (pid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var got int
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		got, sockErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if ctrlErr != nil || sockErr != nil || got <= 0 {
		return 0, false
	}
	return got, true
}

// osPeerStartTime returns the start time (microseconds since the epoch) the
// kernel records for pid, from its kinfo_proc entry. A pid with no entry, or an
// entry for another pid, reports ok=false.
func osPeerStartTime(pid int) (uint64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	st := kp.Proc.P_starttime
	start := uint64(st.Sec)*1_000_000 + uint64(st.Usec)
	return start, start != 0
}
