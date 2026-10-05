//go:build !windows

package runtime

// Purpose: the lifetime lock behind ListenOwnerSocket's staleness proof:
//   a held lock is a conflict even when a connect is refused, the path
//   rebinds once the holder closes, a hostile lock entry is refused and
//   left, and Close leaves a path that no longer names its inode.
// Constraints: untagged and free of the "net" import (internal/build's
//   no-network unit gate): stale sockets are raw bound-then-closed
//   descriptors and every bind goes through the helper. Short os.MkdirTemp
//   roots keep socket names inside sun_path.
// SPORT: internal/runtime (ADD).

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// shortLockDir returns a fresh 0700 directory with a short path.
func shortLockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "col")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// rawStaleSocket leaves a socket at path that nobody listens on (bound and
// closed without listen), so a connect to it is refused.
func rawStaleSocket(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// wantSameEntry fails unless path still names the inode before describes.
func wantSameEntry(t *testing.T, path string, before os.FileInfo) {
	t.Helper()
	if after, err := os.Lstat(path); err != nil || !os.SameFile(before, after) {
		t.Fatalf("%s after the call: %v, %v; want the same entry left", path, after, err)
	}
}

// TestListenOwnerSocketHeldLockIsConflict [R123]: while the lock is held
// the path is never judged stale, even though a connect to the socket there
// is refused (ECONNREFUSED, what a full backlog gives on darwin); once the
// holder lets go the same socket is proven stale and replaced.
func TestListenOwnerSocketHeldLockIsConflict(t *testing.T) {
	path := filepath.Join(shortLockDir(t), "d.sock")
	before := rawStaleSocket(t, path)
	holder, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ln, err := ListenOwnerSocket(path)
	if ln != nil {
		_ = ln.Close()
	}
	wantSocketRefusal(t, err, cascade.KindConflict, "a live listener holds its lock")
	wantSameEntry(t, path, before)
	_ = holder.Close()

	first, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("after the holder closed: %v, want the stale socket replaced", err)
	}
	if fi, lerr := os.Lstat(path); lerr != nil || os.SameFile(before, fi) {
		t.Fatalf("after rebind: %v, %v; want a fresh inode", fi, lerr)
	}
	bound, _ := os.Lstat(path)
	second, err := ListenOwnerSocket(path)
	if second != nil {
		_ = second.Close()
	}
	wantSocketRefusal(t, err, cascade.KindConflict, "a live listener holds its lock")
	wantSameEntry(t, path, bound)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("after the listener closed: %v, want a bind", err)
	}
	_ = third.Close()
}

// TestOwnerSocketLockHostileStates: a lock entry that is a symlink, not a
// regular file, owned by another uid, open to group/other, or hard-linked
// elsewhere is refused with KindPermissionDenied and left, and no socket is
// created.
func TestOwnerSocketLockHostileStates(t *testing.T) {
	euid := os.Geteuid()
	foreignFile := func(fi os.FileInfo) (uint32, bool) {
		if fi.Mode().IsRegular() {
			return uint32(euid + 1), true
		}
		return statOwner(fi)
	}
	cases := []struct {
		name  string
		plant func(dir, lock string) error
		owner SocketOwner
		want  string
	}{
		{"dangling symlink", func(d, l string) error { return os.Symlink(filepath.Join(d, "x"), l) }, statOwner, "is a symlink"},
		{"symlink to own file", func(d, l string) error {
			return firstErr(os.WriteFile(filepath.Join(d, "x"), nil, 0o600), os.Symlink(filepath.Join(d, "x"), l))
		}, statOwner, "is a symlink"},
		{"directory", func(_, l string) error { return os.Mkdir(l, 0o700) }, statOwner, "is not a regular file"},
		{"fifo", func(_, l string) error { return syscall.Mkfifo(l, 0o600) }, statOwner, "is not a regular file"},
		{"foreign owner", func(_, l string) error { return os.WriteFile(l, nil, 0o600) }, foreignFile, "owned by another user"},
		{"open to others", func(_, l string) error { return firstErr(os.WriteFile(l, nil, 0o600), os.Chmod(l, 0o644)) }, statOwner, "open to other users"},
		{"hard-linked", func(d, l string) error {
			return firstErr(os.WriteFile(filepath.Join(d, "x"), nil, 0o600), os.Link(filepath.Join(d, "x"), l))
		}, statOwner, "has other hard links"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := shortLockDir(t)
			path := filepath.Join(dir, "d.sock")
			if err := c.plant(dir, path+".lock"); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path + ".lock")
			_, err := listenOwnerSocket(path, euid, c.owner)
			wantSocketRefusal(t, err, cascade.KindPermissionDenied, c.want)
			wantSameEntry(t, path+".lock", before)
			if _, lerr := os.Lstat(path); lerr == nil {
				t.Fatal("a socket was created beside a refused lock")
			}
		})
	}
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// TestOwnerSocketCloseLeavesSwappedPath: when the path no longer names the
// inode the listener bound, Close leaves it; it also releases the lock, so
// that swapped-in socket (refusing connects) is then proven stale.
func TestOwnerSocketCloseLeavesSwappedPath(t *testing.T) {
	path := filepath.Join(shortLockDir(t), "d.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	swapped := rawStaleSocket(t, path)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	wantSameEntry(t, path, swapped)
	if err := ln.Close(); err == nil {
		t.Fatal("second Close reported success")
	}
	wantSameEntry(t, path, swapped)
	again, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("after Close released the lock: %v, want a bind", err)
	}
	// Close runs once: a later Close never removes path, even when path
	// names the bound inode again (what a reused inode number looks like).
	if err := os.Link(path, path+".keep"); err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
	if err := os.Link(path+".keep", path); err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("a second Close removed the path: %v", err)
	}
}
