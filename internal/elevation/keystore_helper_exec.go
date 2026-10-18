//go:build !windows

// Purpose: the production helperRunner. It starts the helper in its own
//
//	process group with one end of a socketpair as fd 3 (the gate; stdin and
//	stdout are /dev/null, the environment is empty), lets the caller verify
//	the running child while it blocks on the gate, refuses a gate that already
//	holds bytes, and only then opens a unix listener in a fresh 0700
//	directory and tells the child where it is. The request and the reply
//	travel on the connection the child makes to the listener, accepted only
//	from the verified child's pid: bytes written into an inherited descriptor
//	by a program that later became the helper, a connection it made before
//	the listener existed, or a descendant that kept the gate, never reach
//	cascade.
//
// Inputs: the helper path, the request line, a timeout, the child vet hook.
// Outputs: the response line (at most helperMaxLine bytes) or an error; the
//
//	child is always killed and reaped before return.
//
// Constraints: the response read is size-bounded (a hostile helper cannot
//
//	stream gigabytes), the whole exchange is bounded by timeout, and a vet
//	failure means no request byte is ever written.
//
// SPORT: internal/elevation helper runner/ADDED (P1-SHIP-15).

package elevation

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"time"
)

func init() {
	platformHelperRun = execHelper
	platformDirChain = checkHelperLocation
	platformVolumeTrusted = volumeHonoursOwnership
	platformFileOwnerTrusted = func(fi os.FileInfo) bool {
		st, ok := fi.Sys().(*syscall.Stat_t)
		return ok && (int(st.Uid) == os.Geteuid() || st.Uid == 0)
	}
}

// mntIgnoreOwnership is darwin's MNT_IGNORE_OWNERSHIP mount flag ("ignore
// ownership on this volume"): every file there looks owned by the caller, so
// the owner checks prove nothing.
const mntIgnoreOwnership = 0x00200000

// volumeHonoursOwnership reports whether file ownership on path's volume is
// real (darwin reads the mount flags; elsewhere it is always true).
func volumeHonoursOwnership(path string) bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return false
	}
	return reflect.ValueOf(st).FieldByName("Flags").Uint()&mntIgnoreOwnership == 0
}

// socketpair returns (parent, child) ends of a unix stream socketpair; the
// parent end is non-blocking so read and write deadlines work.
func socketpair() (parent, child *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		_ = syscall.Close(fds[0])
		_ = syscall.Close(fds[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "helper-parent"), os.NewFile(uintptr(fds[1]), "helper-child"), nil
}

func execHelper(ctx context.Context, path string, req []byte, timeout time.Duration, vetChild func(pid int) error) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	gate, gateEnd, err := socketpair()
	if err != nil {
		return nil, err
	}
	defer func() { _ = gate.Close() }()
	cmd, err := startHelper(ctx, path, gateEnd)
	if err != nil {
		return nil, err
	}
	defer reapGroup(cmd)
	if err := vetChild(cmd.Process.Pid); err != nil {
		return nil, err
	}
	if !socketQuiet(gate) {
		return nil, errors.New("helper wrote or closed the gate before it was asked")
	}
	// The listener does not exist until the running child has been verified:
	// a program that connected to it before becoming the helper (same pid,
	// new image) could otherwise carry a pre-queued reply across its exec.
	// From here the child's pid is the genuine image for good, so a
	// connection from that pid is the helper; any other pid is refused.
	ln, closeListener, err := listenPrivate()
	if err != nil {
		return nil, err
	}
	defer closeListener()
	deadline, _ := ctx.Deadline()
	_ = gate.SetDeadline(deadline)
	// Only now does anyone learn where the listener is.
	if _, err := gate.Write([]byte(ln.Addr().String() + "\n")); err != nil {
		return nil, err
	}
	conn, err := acceptFromPID(ln, deadline, cmd.Process.Pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(append(append([]byte{}, req...), '\n')); err != nil {
		return nil, err
	}
	return readHelperLine(conn)
}

// listenPrivate opens a unix listener in a fresh directory (mode 0700, owned
// by this user) and returns it with the function that removes both.
func listenPrivate() (*net.UnixListener, func(), error) {
	base := os.TempDir()
	if err := checkPrivateBase(base); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(base, "cascade-elevate-")
	if err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(dir, "s")
	if len(sock) > maxSocketPath {
		_ = os.RemoveAll(dir)
		return nil, nil, errors.New("temp directory path is too long for a unix socket")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return ln.(*net.UnixListener), func() { _ = ln.Close(); _ = os.RemoveAll(dir) }, nil
}

// maxSocketPath keeps the socket path under darwin's 104-byte sun_path.
const maxSocketPath = 100

// startHelper starts the helper in its own process group with an empty
// environment, working directory / and the gate end as fd 3; stdin, stdout and
// stderr are /dev/null. The parent's copy of the gate end is closed.
func startHelper(ctx context.Context, path string, gateEnd *os.File) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{gateEnd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Start()
	_ = gateEnd.Close()
	return cmd, err
}

// reapGroup kills the helper's whole process group (so no descendant outlives
// the exchange), then the helper itself, and waits for it.
func reapGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// acceptFromPID accepts connections until one comes from process pid (the
// verified child); connections from any other process are closed unread.
func acceptFromPID(ln *net.UnixListener, deadline time.Time, pid int) (net.Conn, error) {
	_ = ln.SetDeadline(deadline)
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if got, perr := peerPID(conn); perr == nil && got == pid {
			return conn, nil
		}
		_ = conn.Close()
	}
}

// peerPID returns the pid of the process on the other end of conn.
func peerPID(conn *net.UnixConn) (int, error) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid, gerr := 0, errors.New("peer pid is not supported on this platform")
	cerr := rc.Control(func(fd uintptr) {
		switch runtime.GOOS {
		case "darwin": // SOL_LOCAL, LOCAL_PEERPID
			pid, gerr = syscall.GetsockoptInt(int(fd), 0, 2)
		case "linux": // SOL_SOCKET, SO_PEERCRED: the leading pid_t of struct ucred
			pid, gerr = syscall.GetsockoptInt(int(fd), 1, 17)
		}
	})
	if cerr != nil {
		return 0, cerr
	}
	return pid, gerr
}

// socketQuiet reports whether nothing is readable on the parent end and the
// child end is still open: a program that wrote into the socket and then
// became the verified helper (exec under the same pid) is refused.
func socketQuiet(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	quiet := false
	_ = rc.Read(func(fd uintptr) bool {
		var b [1]byte
		_, _, rerr := syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		quiet = rerr == syscall.EAGAIN
		return true
	})
	return quiet
}

// readHelperLine reads one newline-terminated line of at most helperMaxLine
// bytes.
func readHelperLine(r io.Reader) ([]byte, error) {
	line, err := bufio.NewReader(io.LimitReader(r, helperMaxLine+1)).ReadBytes('\n')
	if len(line) > helperMaxLine {
		return nil, errors.New("oversize response line")
	}
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return line, nil
}
