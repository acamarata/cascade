//go:build windows

package runtime

// Purpose: the Windows half of ListenOwnerSocket (see socket_unix.go).
// Inputs: the socket path.
// Outputs: a listening AF_UNIX net.Listener, or a *cascade.Error:
//   KindPermissionDenied (the path is a symlink or not a socket),
//   KindConflict (a live listener answers, or the socket cannot be proven
//   stale), KindUnavailable (mkdir, remove or listen failed).
// Constraints: symlink and non-socket refusal only. The owner-uid, directory
//   and 0600 rules are unix-only: Windows has no uid or mode bits for them,
//   and the socket inherits its directory's ACL. Lstat, never Stat. The
//   unix lifetime lock (socket_lock_unix.go) is not ported: the packet keeps
//   Windows to symlink and non-socket refusal, so a refused connect is still
//   the stale proof here, with the full-backlog residual documented in
//   docs/security-posture/control-sockets.md.
// SPORT: internal/runtime (ADD).

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// socketProbeTimeout bounds the one connect used to tell a live socket from
// a stale one.
const socketProbeTimeout = 2 * time.Second

// wsaeConnRefused is Winsock's WSAECONNREFUSED. syscall.ECONNREFUSED on
// Windows is an invented value the net package never returns.
const wsaeConnRefused = syscall.Errno(10061)

// ListenOwnerSocket binds an AF_UNIX socket at path after refusing a
// symlink or a non-socket there and removing only a stale socket.
func ListenOwnerSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: create directory", path)
	}
	if err := clearWindowsSocketPath(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: listen", path)
	}
	return ln, nil
}

// clearWindowsSocketPath leaves path absent or fails: a symlink or a
// non-socket is refused and left; a socket that answers, or whose connect
// fails other than by refusal, is a conflict; only a refused one is removed.
func clearWindowsSocketPath(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s", path)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: socket %s is a symlink; refusing it", path)
	case fi.Mode()&os.ModeSocket == 0:
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: socket %s exists and is not a socket; refusing it", path)
	}
	c, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err == nil {
		_ = c.Close()
		return cascade.Newf(cascade.KindConflict, "runtime: socket %s: a live listener answers", path)
	}
	if !errors.Is(err, wsaeConnRefused) {
		return cascade.Wrapf(cascade.KindConflict, err, "runtime: socket %s: cannot prove it stale", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: remove stale socket", path)
	}
	return nil
}

// probeSocketLocked exists so the shared crash-recovery scan compiles: Scan
// returns before its socket probe on Windows, which has no socket lock.
func probeSocketLocked(path string, _ time.Duration, _ Dialer) (live, stale bool, release func(), err error) {
	return false, false, func() {}, cascade.Newf(cascade.KindUnavailable,
		"runtime: recovery: socket %s: no socket lock on windows", path)
}
