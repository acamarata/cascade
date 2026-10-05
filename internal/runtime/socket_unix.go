//go:build !windows

package runtime

// Purpose: ListenOwnerSocket, the one owner-only unix socket bind both the
//   daemon socket and `cascade mcp serve --socket` use, split into
//   PrepareOwnerSocket (lock, private bind, 0600, listen) and Publish (the
//   link), so the daemon writes its pidfile before path can be dialed.
// Inputs: the socket path; the effective uid and a SocketOwner that reads a
//   file's owning uid (both injected by tests through listenOwnerSocket).
// Outputs: a listening net.Listener whose socket file is mode 0600 and owned
//   by the euid, or a *cascade.Error: KindPermissionDenied (the directory
//   fails the owner rule; the path or its lock file is a symlink, the wrong
//   type, owned by another uid, or the lock is open to others or has other
//   links), KindConflict (another listener holds the lock or answers, or
//   the socket cannot be proven stale), KindUnavailable (a name too long, or
//   mkdir, open, bind or listen failed).
// Constraints: staleness is proven only by taking flock(LOCK_EX|LOCK_NB) on
//   "<path>.lock", which every listener holds for its whole lifetime and
//   releases on Close; a refused connect alone proves nothing (a full
//   backlog refuses one on darwin). The lock and the listener that holds it
//   live in socket_lock_unix.go. Lstat, never Stat, on the path.
//   The directory rule makes the Lstat-then-remove safe: only the euid can
//   replace an entry in an owner-only directory, only the entry's owner in a
//   sticky one. The socket is bound in a fresh private 0700 directory beside
//   path, tightened to 0600 and listening there, then hard-linked to path:
//   nothing can connect before 0600, the path never names a socket that is
//   not listening, and a symlink planted at path is refused, never followed
//   (bind(2) follows one on darwin; link(2) never does). No umask change.
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

// SocketOwner reports the uid that owns the file fi describes; ok is false
// when the platform's stat data carries no uid. Tests inject one to stand in
// for a foreign owner without chown or root.
type SocketOwner func(fi os.FileInfo) (uid uint32, ok bool)

// socketProbeTimeout bounds the one connect made with the lock held; it
// catches a listener that never took the lock (a pre-lock binary).
const socketProbeTimeout = 2 * time.Second

// privNameExtra is how much longer than the directory the private bind name
// can be: "/." + MkdirTemp's random part (a uint32, at most 10 digits) + "/s".
const privNameExtra = 2 + 10 + 2

// socketStageHook is a test seam called with "chmod" (the private name)
// between bind and chmod, "listening" (the private name) once the socket
// listens and before the link, and "link" (path) just before the link.
var socketStageHook = func(_, _ string) {}

// ListenOwnerSocket binds an owner-only unix socket at path for the current
// effective uid: PrepareOwnerSocket then Publish. See this file's header for
// every refusal.
func ListenOwnerSocket(path string) (net.Listener, error) {
	return listenOwnerSocket(path, os.Geteuid(), statOwner)
}

// PrepareOwnerSocket runs every ListenOwnerSocket step but the final link:
// it holds path's lock and listens under a private name, so another start
// conflicts while nothing can dial path. Exactly one of Publish or Abort
// must follow.
func PrepareOwnerSocket(path string) (*PreparedSocket, error) {
	return prepareOwnerSocket(path, os.Geteuid(), statOwner)
}

// listenOwnerSocket is ListenOwnerSocket with the euid and owner lookup
// injected: prepare, then publish, aborting on a failed publish.
func listenOwnerSocket(path string, euid int, owner SocketOwner) (net.Listener, error) {
	p, err := prepareOwnerSocket(path, euid, owner)
	if err != nil {
		return nil, err
	}
	ln, err := p.Publish()
	if err != nil {
		p.Abort()
		return nil, err
	}
	return ln, nil
}

// prepareOwnerSocket takes the lock before the path is judged and hands it
// to the prepared listener, or releases it on any failure.
func prepareOwnerSocket(path string, euid int, owner SocketOwner) (*PreparedSocket, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: create directory", path)
	}
	if err := checkSocketDir(dir, euid, owner); err != nil {
		return nil, err
	}
	lock, err := lockSocketPath(path, euid, owner)
	if err != nil {
		return nil, err
	}
	var p *PreparedSocket
	if err = clearSocketPath(path, euid, owner); err == nil {
		p, err = bindOwnerSocket(path, euid, owner, lock)
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return p, nil
}

// PreparedSocket is a secured socket listening under a private name beside
// its path while it holds the path's lock.
type PreparedSocket struct {
	ln        *ownerSocketListener
	tmp, priv string
	euid      int
	owner     SocketOwner
}

// Publish hard-links the prepared inode to its path (linkSocket's rules)
// and drops the private name. On failure nothing is released: the caller
// still holds the lock and must call Abort.
func (p *PreparedSocket) Publish() (net.Listener, error) {
	if err := linkSocket(p.tmp, p.ln.path, p.ln.inode, p.euid, p.owner); err != nil {
		return nil, err
	}
	p.dropPrivate()
	return p.ln, nil
}

// Abort discards an unpublished socket: it stops listening, drops the
// private name and releases the lock. The path is never touched.
func (p *PreparedSocket) Abort() {
	_ = p.ln.Listener.Close()
	p.dropPrivate()
	_ = p.ln.lock.Close()
}

// dropPrivate removes the private bind name and its directory.
func (p *PreparedSocket) dropPrivate() { _ = os.Remove(p.tmp); _ = os.Remove(p.priv) }

// statOwner reads the owning uid from the unix stat data.
func statOwner(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// checkSocketDir applies the directory rule to dir, pre-existing or just
// created: owned by the euid, or by root with the sticky bit, and not
// group/other-writable unless sticky. Ancestors are trusted, not checked.
func checkSocketDir(dir string, euid int, owner SocketOwner) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket directory %s", dir)
	}
	sticky := fi.Mode()&os.ModeSticky != 0
	uid, ok := owner(fi)
	if !ok || (int(uid) != euid && (uid != 0 || !sticky)) {
		return cascade.Newf(cascade.KindPermissionDenied,
			"runtime: socket directory %s is not owned by you (or by root with the sticky bit)", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 && !sticky {
		return cascade.Newf(cascade.KindPermissionDenied,
			"runtime: socket directory %s is group/other-writable without the sticky bit", dir)
	}
	return nil
}

// classifyPathEntry judges an existing entry from its Lstat (or fstat) result:
// a symlink, a type other than want (os.ModeSocket, or 0 for a regular
// file), or an entry another uid owns is refused. what names it.
func classifyPathEntry(what, path string, fi os.FileInfo, want os.FileMode, euid int, owner SocketOwner) error {
	kind := map[os.FileMode]string{os.ModeSocket: "socket", 0: "regular file"}[want]
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: %s %s is a symlink; refusing it", what, path)
	case fi.Mode().Type() != want:
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: %s %s exists and is not a %s; refusing it", what, path, kind)
	}
	if uid, ok := owner(fi); !ok || int(uid) != euid {
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: %s %s is owned by another user; refusing it", what, path)
	}
	return nil
}

// classifySocketEntry is classifyPathEntry for the socket path itself.
func classifySocketEntry(path string, fi os.FileInfo, euid int, owner SocketOwner) error {
	return classifyPathEntry("socket", path, fi, os.ModeSocket, euid, owner)
}

// bindOwnerSocket creates the socket in a fresh private directory beside
// path, where nobody else can plant an entry: bind, confirm and tighten the
// inode, then listen. The listener owns lock; Publish links it into place.
func bindOwnerSocket(path string, euid int, owner SocketOwner, lock *os.File) (*PreparedSocket, error) {
	limit := len(syscall.RawSockaddrUnix{}.Path)
	if priv := len(filepath.Dir(path)) + privNameExtra; len(path) >= limit || priv >= limit {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"runtime: socket %s: path too long for a unix socket (it and its %d-byte private bind name must each be under %d bytes)",
			path, priv, limit)
	}
	priv, err := os.MkdirTemp(filepath.Dir(path), ".")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: private directory", path)
	}
	tmp := filepath.Join(priv, "s")
	p := &PreparedSocket{tmp: tmp, priv: priv, euid: euid, owner: owner}
	fd, err := boundSocket(tmp, path)
	if err != nil {
		p.dropPrivate()
		return nil, err
	}
	fi, err := secureSocketInode(tmp, path, euid, owner)
	if err == nil {
		if lerr := syscall.Listen(fd, syscall.SOMAXCONN); lerr != nil {
			err = cascade.Wrapf(cascade.KindUnavailable, lerr, "runtime: socket %s: listen", path)
		}
	}
	if err != nil {
		_ = syscall.Close(fd)
		p.dropPrivate()
		return nil, err
	}
	if p.ln, err = newOwnerSocketListener(fd, path, fi, lock); err != nil {
		p.dropPrivate()
		return nil, err
	}
	socketStageHook("listening", tmp)
	return p, nil
}

// boundSocket returns a close-on-exec unix stream socket bound to tmp.
func boundSocket(tmp, path string) (int, error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: create", path)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: tmp}); err != nil {
		_ = syscall.Close(fd)
		return -1, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: bind", path)
	}
	return fd, nil
}

// secureSocketInode confirms with Lstat that tmp is a socket the euid owns,
// sets mode 0600 and checks the same inode now carries exactly 0600.
func secureSocketInode(tmp, path string, euid int, owner SocketOwner) (os.FileInfo, error) {
	fi, err := os.Lstat(tmp)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: after bind", path)
	}
	if err := classifySocketEntry(path, fi, euid, owner); err != nil {
		return nil, err
	}
	socketStageHook("chmod", tmp)
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: chmod 0600", path)
	}
	after, err := os.Lstat(tmp)
	if err != nil || !os.SameFile(fi, after) || after.Mode().Perm() != 0o600 {
		return nil, cascade.Newf(cascade.KindPermissionDenied, "runtime: socket %s changed while it was being secured", path)
	}
	return after, nil
}

// linkSocket hard-links the secured inode at tmp to path. An entry that
// appeared at path after clearSocketPath (EEXIST) is judged once more by
// the same rules and the link retried once; path must then name our inode.
func linkSocket(tmp, path string, fi os.FileInfo, euid int, owner SocketOwner) error {
	socketStageHook("link", path)
	err := os.Link(tmp, path)
	if errors.Is(err, fs.ErrExist) {
		if err = clearSocketPath(path, euid, owner); err == nil {
			err = os.Link(tmp, path)
		}
	}
	if err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return err
		}
		return cascade.Wrapf(cascade.KindUnavailable, err, "runtime: socket %s: link into place", path)
	}
	if linked, lerr := os.Lstat(path); lerr != nil || !os.SameFile(fi, linked) {
		return cascade.Newf(cascade.KindPermissionDenied, "runtime: socket %s changed while it was being secured", path)
	}
	return nil
}
