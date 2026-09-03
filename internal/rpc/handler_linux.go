//go:build linux

package rpc

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// osGetuid is the production ownerUID implementation.
func osGetuid() int { return os.Getuid() }

// peerCredFromConn resolves conn's peer UID via SO_PEERCRED (Linux). conn
// must be a *net.UnixConn (the only listener type the daemon socket uses);
// any other type reports ok=false, fail-closed.
func peerCredFromConn(conn net.Conn) (uid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if ctrlErr != nil || sockErr != nil || cred == nil {
		return 0, false
	}
	return int(cred.Uid), true
}
