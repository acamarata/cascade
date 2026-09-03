//go:build !linux && !darwin

package rpc

import (
	"net"
	"os"
)

// osGetuid is the production ownerUID implementation.
func osGetuid() int { return os.Getuid() }

// peerCredFromConn has no peer-credential syscall implemented for this
// platform (Windows never reaches here — the daemon does not run at all on
// Windows, tier-2; any other GOOS, e.g. freebsd/openbsd, is compile-only,
// not a release platform). It fails closed: ok=false, which Handler treats
// as 403, never as an implicit allow.
func peerCredFromConn(conn net.Conn) (uid int, ok bool) {
	return 0, false
}
