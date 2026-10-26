//go:build linux

package rpc

import (
	"bytes"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// osGetuid is the production ownerUID implementation.
func osGetuid() int { return os.Getuid() }

// peerUcred reads conn's SO_PEERCRED record. conn must be a *net.UnixConn
// (the only listener type the daemon socket uses); any other type, or any
// syscall failure, reports nil, fail-closed.
func peerUcred(conn net.Conn) *unix.Ucred {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	var cred *unix.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil {
		return nil
	}
	return cred
}

// peerCredFromConn resolves conn's peer UID via SO_PEERCRED (Linux).
func peerCredFromConn(conn net.Conn) (uid int, ok bool) {
	cred := peerUcred(conn)
	if cred == nil {
		return 0, false
	}
	return int(cred.Uid), true
}

// peerPIDFromConn resolves the pid of the process that connected conn, from
// the same SO_PEERCRED record. The kernel reports pid 0 for a peer in another
// pid namespace, which reads as unknown.
func peerPIDFromConn(conn net.Conn) (pid int, ok bool) {
	cred := peerUcred(conn)
	if cred == nil || cred.Pid <= 0 {
		return 0, false
	}
	return int(cred.Pid), true
}

// osPeerStartTime reads the start time (field 22 of /proc/<pid>/stat, clock
// ticks since boot) of pid. The command name sits between parentheses and may
// itself hold spaces and parentheses, so the fields start after the LAST ')'.
// Together with the pid it identifies one process for the life of the boot.
func osPeerStartTime(pid int) (uint64, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return 0, false
	}
	fields := bytes.Fields(raw[i+1:])
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseUint(string(fields[19]), 10, 64)
	return start, err == nil && start != 0
}
