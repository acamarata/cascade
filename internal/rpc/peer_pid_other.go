//go:build !linux && !darwin

package rpc

import "net"

// peerPIDFromConn has no peer-pid syscall on this platform (Windows never
// reaches here: the daemon does not run there; any other GOOS is compile-only).
// It reports ok=false, which guardOwnerPeer treats as a refusal, never as an
// implicit allow.
func peerPIDFromConn(_ net.Conn) (pid int, ok bool) {
	return 0, false
}

// osPeerStartTime has no implementation here; with no peer pid it is never asked.
func osPeerStartTime(_ int) (uint64, bool) { return 0, false }
