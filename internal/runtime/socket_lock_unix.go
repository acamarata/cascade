//go:build !windows

package runtime

// Purpose: the lifetime lock behind ListenOwnerSocket's staleness proof,
//   the checks made while it is held (clearSocketPath, provenStale), the
//   listener that holds it (see socket_unix.go for the bind itself), and
//   the crash-recovery scan's entry to the same lock (recovery_scan.go).
// Inputs: the socket path, the effective uid and a SocketOwner.
// Outputs: an open "<path>.lock" holding flock(LOCK_EX|LOCK_NB), or a
//   *cascade.Error: KindPermissionDenied (the lock entry is a symlink, not a
//   regular file, owned by another uid, open to group/other, or has other
//   hard links; it is refused and left), KindConflict (another listener
//   holds the lock), KindUnavailable (open, stat or flock failed).
// Constraints: every listener holds its lock until Close and the lock file
//   is never removed: unlinking it would let a second process create and
//   lock a fresh inode while the first still holds the old one. O_NOFOLLOW
//   refuses a symlink at the lock path; O_NONBLOCK keeps a planted FIFO from
//   hanging the open. flock locks belong to the open file description, so a
//   second open in the same process conflicts too.
// SPORT: internal/runtime (ADD).

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// classifyLockEntry refuses a lock file that is not a regular file the euid
// owns, that grants any group/other bit (another user could open and hold
// it), or that has other hard links (it would be some other file of ours).
func classifyLockEntry(name string, fi os.FileInfo, euid int, owner SocketOwner) error {
	if err := classifyPathEntry("socket lock", name, fi, 0, euid, owner); err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: socket lock %s is open to other users; refusing it", name)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: socket lock %s has other hard links; refusing it", name)
	}
	return nil
}

// lockSocketPath opens "<path>.lock" (O_NOFOLLOW, O_CREAT, 0600; O_NONBLOCK
// so a planted FIFO cannot hang the open), judges it, and takes a
// non-blocking exclusive flock. A held lock is a live listener: a conflict.
// A hostile lock entry is refused and left.
func lockSocketPath(path string, euid int, owner SocketOwner) (*os.File, error) {
	name := path + ".lock"
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		if fi, lerr := os.Lstat(name); lerr == nil {
			if cerr := classifyLockEntry(name, fi, euid, owner); cerr != nil {
				return nil, cerr
			}
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket lock %s: open", name)
	}
	fi, err := f.Stat()
	if err != nil {
		err = cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket lock %s: stat", name)
	} else {
		err = classifyLockEntry(name, fi, euid, owner)
	}
	if err == nil {
		if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(ferr, syscall.EWOULDBLOCK) {
			err = cascade.Newf(cascade.KindConflict, "runtime: socket %s: a live listener holds its lock %s", path, name)
		} else if ferr != nil {
			err = cascade.Wrapf(cascade.KindUnavailable, ferr, "runtime: socket lock %s: flock", name)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ownerSocketListener reports path as its address and holds the lock until
// Close, which removes path only while it still names the bound inode.
type ownerSocketListener struct {
	net.Listener
	path  string
	inode os.FileInfo
	lock  *os.File
	once  sync.Once
}

// newOwnerSocketListener wraps the listening descriptor fd (always consumed).
func newOwnerSocketListener(fd int, path string, fi os.FileInfo, lock *os.File) (*ownerSocketListener, error) {
	f := os.NewFile(uintptr(fd), path)
	ln, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: listener", path)
	}
	return &ownerSocketListener{Listener: ln, path: path, inode: fi, lock: lock}, nil
}

// Addr is the socket path, not the private name the inode was bound under.
func (l *ownerSocketListener) Addr() net.Addr { return &net.UnixAddr{Name: l.path, Net: "unix"} }

// Close runs once: it judges path while the listening descriptor still pins
// our inode (so its number cannot be reused by a successor), removes path
// only if it is still ours, stops listening, then releases the lock.
func (l *ownerSocketListener) Close() error {
	err := net.ErrClosed
	l.once.Do(func() {
		if fi, lerr := os.Lstat(l.path); lerr == nil && os.SameFile(fi, l.inode) {
			_ = os.Remove(l.path)
		}
		err = l.Listener.Close()
		_ = l.lock.Close()
	})
	return err
}

// lockForRecovery is the crash-recovery scan's entry to the lifetime lock:
// the listener's own directory rule and lockSocketPath, then path's Lstat
// taken while the lock is held (nil when path is gone). Its only
// KindConflict is a lock another listener holds: a live daemon. The caller
// closes the returned lock; nothing is removed here.
func lockForRecovery(path string) (*os.File, os.FileInfo, error) {
	euid := os.Geteuid()
	if err := checkSocketDir(filepath.Dir(path), euid, statOwner); err != nil {
		return nil, nil, err
	}
	lock, err := lockSocketPath(path, euid, statOwner)
	if err != nil {
		return nil, nil, err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return lock, nil, nil
	}
	if err != nil {
		_ = lock.Close()
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: recovery: socket %s", path)
	}
	return lock, fi, nil
}

// probeSocketLocked is the crash-recovery scan's socket probe (R127). It
// returns release, never nil, which Scan runs when its whole scan is done,
// so a removal it decides on happens with the lock still held. A missing
// path is the clean start: nothing is locked. A lock another listener holds
// is a live daemon (live=true) whatever a connect would say. With the lock
// taken, anything but our own socket is refused and left (a symlink only
// after its dial is refused, so R-14.161's undecidable report holds), and
// probeSocket's dial decides: an answer is a lock-less live listener, a
// refusal is stale (the lock proves nobody holds it), anything else is an
// undecidable error.
func probeSocketLocked(path string, timeout time.Duration, dial Dialer) (live, stale bool, release func(), err error) {
	release = func() {}
	if _, lerr := os.Lstat(path); errors.Is(lerr, fs.ErrNotExist) {
		return false, false, release, nil
	}
	lock, fi, err := lockForRecovery(path)
	if cascade.HasKind(err, cascade.KindConflict) {
		return true, false, release, nil
	}
	if err != nil {
		return false, false, release, err
	}
	release = func() { _ = lock.Close() }
	if fi == nil { // the holder closed, and so removed it, before we locked
		return false, false, release, nil
	}
	link := fi.Mode()&os.ModeSymlink != 0
	if !link {
		if err := judgeRecoveryEntry(path, fi); err != nil {
			return false, false, release, err
		}
	}
	live, stale, err = probeSocket(path, timeout, dial)
	if stale && link {
		return false, false, release, judgeRecoveryEntry(path, fi)
	}
	return live, stale, release, err
}

// judgeRecoveryEntry refuses, and so leaves, an entry the recovery scan may
// not remove; see judgeRecoveryEntryAs.
func judgeRecoveryEntry(path string, fi os.FileInfo) error {
	return judgeRecoveryEntryAs(path, fi, os.Geteuid(), statOwner)
}

// judgeRecoveryEntryAs refuses anything at path that is not a socket
// (KindConflict: a regular file, directory, FIFO or symlink is never scan
// debris) and a socket another uid owns (KindPermissionDenied, the
// listener's own rule), so the scan only ever removes our own socket.
func judgeRecoveryEntryAs(path string, fi os.FileInfo, euid int, owner SocketOwner) error {
	if fi.Mode().Type() != os.ModeSocket {
		return cascade.Newf(cascade.KindConflict,
			"runtime: recovery: %s exists and is not a socket; refusing it and leaving it", path)
	}
	return classifySocketEntry(path, fi, euid, owner)
}

// clearSocketPath, called with the lock held, leaves path absent or fails:
// nothing there is fine; our own socket is stale and removed unless a
// lock-less listener answers on it; anything else is refused and left.
func clearSocketPath(path string, euid int, owner SocketOwner) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s", path)
	}
	if err := classifySocketEntry(path, fi, euid, owner); err != nil {
		return err
	}
	if err := provenStale(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: remove stale socket", path)
	}
	return nil
}

// provenStale is the second check, made only with the lock held: an answer
// is a conflict and any failure other than a refusal proves nothing. It is
// never the staleness proof on its own; the lock is.
func provenStale(path string) error {
	c, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err == nil {
		_ = c.Close()
		return cascade.Newf(cascade.KindConflict, "runtime: socket %s: a live listener answers", path)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return nil
	}
	return cascade.Wrapf(cascade.KindConflict, err, "runtime: socket %s: cannot prove it stale", path)
}
