//go:build !windows

package runtime

// Purpose: ListenOwnerSocket's refusal and classification paths that need
//   no bound socket (the directory rule, a symlink or regular file at the
//   path, a foreign owner on a socket-shaped FileInfo, the owner lookup, an
//   unprovable stale probe, the failures before any socket exists), plus one
//   round trip through the helper's own bind path for the untagged coverage
//   ratchet.
// Constraints: untagged and free of the "net" import (internal/build's
//   no-network unit gate), the pattern internal/daemon's socket tests use.
//   Only TestOwnerSocketBindsReplacesAndConflicts binds, and only through
//   ListenOwnerSocket itself; the cases needing a peer, a symlink to a live
//   socket or an injected race live in socket_unix_test.go behind the
//   integration tag.
// SPORT: internal/runtime (ADD).

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSocketInfo is an os.FileInfo carrying only a mode.
type fakeSocketInfo struct{ mode os.FileMode }

func (f fakeSocketInfo) Name() string       { return "s" }
func (f fakeSocketInfo) Size() int64        { return 0 }
func (f fakeSocketInfo) Mode() os.FileMode  { return f.mode }
func (f fakeSocketInfo) ModTime() time.Time { return time.Time{} }
func (f fakeSocketInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeSocketInfo) Sys() any           { return nil }

// wantSocketRefusal fails unless err carries kind and a message containing
// want: the kind alone matches every refusal of that kind.
func wantSocketRefusal(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want kind %v with %q", err, kind, want)
	}
}

// skipRootOwnedAsRoot skips a case whose root owner is meant to be someone
// else: running as root, root is the euid.
func skipRootOwnedAsRoot(t *testing.T, name string, euid int) {
	t.Helper()
	if euid == 0 && strings.HasPrefix(name, "root ") && !strings.Contains(name, "sticky") {
		t.Skip("running as root: a root owner is the euid")
	}
}

// uidOwner reports uid for every file.
func uidOwner(uid uint32) SocketOwner {
	return func(os.FileInfo) (uint32, bool) { return uid, true }
}

func TestListenOwnerSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(shortLockDir(t), "d.sock")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := ListenOwnerSocket(path)
	if ln != nil {
		_ = ln.Close()
	}
	wantSocketRefusal(t, err, cascade.KindPermissionDenied, "is not a socket")
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "keep me" {
		t.Fatalf("regular file after refusal = %q, %v; want it unchanged", got, rerr)
	}
	// The refusal released the lock: with the file gone the path binds.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if ln, err = ListenOwnerSocket(path); err != nil {
		t.Fatalf("rebind after removing the file: %v; want a bind", err)
	}
	_ = ln.Close()
}

func TestOwnerSocketDirRule(t *testing.T) {
	euid := os.Geteuid()
	cases := []struct {
		name  string
		mode  os.FileMode
		owner SocketOwner
		want  string // empty: allowed
	}{
		{"owner 0700", 0o700, statOwner, ""},
		{"owner 0755", 0o755, statOwner, ""},
		{"owner sticky 1777", 0o777 | os.ModeSticky, statOwner, ""},
		{"owner 0777", 0o777, statOwner, "group/other-writable"},
		{"owner 0770", 0o770, statOwner, "group/other-writable"},
		{"owner 0720", 0o720, statOwner, "group/other-writable"},
		{"root sticky 1777", 0o777 | os.ModeSticky, uidOwner(0), ""},
		{"root 0755", 0o755, uidOwner(0), "not owned by you"}, // skipped as root
		{"foreign 0700", 0o700, uidOwner(uint32(euid + 1)), "not owned by you"},
		{"foreign sticky 1777", 0o777 | os.ModeSticky, uidOwner(uint32(euid + 1)), "not owned by you"},
		{"no uid", 0o700, func(os.FileInfo) (uint32, bool) { return 0, false }, "not owned by you"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			skipRootOwnedAsRoot(t, c.name, euid)
			dir := filepath.Join(t.TempDir(), "s")
			if err := os.Mkdir(dir, 0o700); err != nil || os.Chmod(dir, c.mode) != nil {
				t.Fatalf("set up %s", dir)
			}
			err := checkSocketDir(dir, euid, c.owner)
			if c.want == "" {
				if err != nil {
					t.Fatalf("checkSocketDir: %v, want allowed", err)
				}
				return
			}
			wantSocketRefusal(t, err, cascade.KindPermissionDenied, c.want)
			_, lerr := listenOwnerSocket(filepath.Join(dir, "d.sock"), euid, c.owner)
			wantSocketRefusal(t, lerr, cascade.KindPermissionDenied, c.want)
			if entries, rerr := os.ReadDir(dir); rerr != nil || len(entries) != 0 {
				t.Fatalf("directory after refusal holds %v (%v), want nothing created", entries, rerr)
			}
		})
	}
}

func TestOwnerSocketClassifyEntry(t *testing.T) {
	euid := os.Geteuid()
	sock := fakeSocketInfo{mode: os.ModeSocket | 0o600}
	cases := []struct {
		name  string
		fi    os.FileInfo
		owner SocketOwner
		want  string // empty: our own socket
	}{
		{"symlink", fakeSocketInfo{mode: os.ModeSymlink | 0o777}, uidOwner(uint32(euid)), "is a symlink"},
		{"regular file", fakeSocketInfo{mode: 0o600}, uidOwner(uint32(euid)), "is not a socket"},
		{"directory", fakeSocketInfo{mode: os.ModeDir | 0o700}, uidOwner(uint32(euid)), "is not a socket"},
		{"foreign socket", sock, uidOwner(uint32(euid + 1)), "owned by another user"},
		{"root socket", sock, uidOwner(0), "owned by another user"},
		{"no uid", sock, func(os.FileInfo) (uint32, bool) { return 0, false }, "owned by another user"},
		{"own socket", sock, uidOwner(uint32(euid)), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			skipRootOwnedAsRoot(t, c.name, euid)
			err := classifySocketEntry("/x/d.sock", c.fi, euid, c.owner)
			if c.want == "" {
				if err != nil {
					t.Fatalf("classifySocketEntry: %v, want nil", err)
				}
				return
			}
			wantSocketRefusal(t, err, cascade.KindPermissionDenied, c.want)
		})
	}
}

func TestOwnerSocketStatOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if uid, ok := statOwner(fi); !ok || int(uid) != os.Geteuid() {
		t.Fatalf("statOwner = %d, %v; want euid %d, true", uid, ok, os.Geteuid())
	}
	if _, ok := statOwner(fakeSocketInfo{}); ok {
		t.Fatal("statOwner on a FileInfo without stat data reported ok")
	}
}

func TestOwnerSocketRefusesDanglingSymlinkBeforeBind(t *testing.T) {
	dir := t.TempDir()
	path, target := filepath.Join(dir, "d.sock"), filepath.Join(dir, "missing")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := ListenOwnerSocket(path)
	wantSocketRefusal(t, err, cascade.KindPermissionDenied, "is a symlink")
	if got, rerr := os.Readlink(path); rerr != nil || got != target {
		t.Fatalf("symlink after refusal -> %q (%v), want %q", got, rerr, target)
	}
	if _, serr := os.Lstat(target); serr == nil {
		t.Fatal("the symlink target was created")
	}
}

func TestOwnerSocketUnprovableStaleIsConflict(t *testing.T) {
	err := provenStale(filepath.Join(t.TempDir(), "absent.sock"))
	wantSocketRefusal(t, err, cascade.KindConflict, "cannot prove it stale")
}

func TestOwnerSocketPreBindFailuresAreUnavailable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ListenOwnerSocket(filepath.Join(blocker, "d.sock"))
	wantSocketRefusal(t, err, cascade.KindUnavailable, "create directory")

	// A name sockaddr_un cannot hold is refused before any socket exists:
	// link(2) would otherwise place a socket no client can dial.
	long := filepath.Join(t.TempDir(), strings.Repeat("s", 120))
	_, err = ListenOwnerSocket(long)
	wantSocketRefusal(t, err, cascade.KindUnavailable, "path too long for a unix socket")
	if _, serr := os.Lstat(long); serr == nil {
		t.Fatal("a socket file was created for an over-long path")
	}
	// The path fits but the private bind name beside it would not: refused
	// by name, not left to fail inside bind(2).
	limit := len(syscall.RawSockaddrUnix{}.Path)
	base, err := os.MkdirTemp("/tmp", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, strings.Repeat("p", limit-privNameExtra-len(base)-1))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = ListenOwnerSocket(filepath.Join(dir, "s"))
	wantSocketRefusal(t, err, cascade.KindUnavailable, "private bind name must each be under")
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("directory after the length refusal holds %v, want only the lock", entries)
	}

	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory is still writable")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	_, err = ListenOwnerSocket(filepath.Join(ro, "d.sock"))
	wantSocketRefusal(t, err, cascade.KindUnavailable, "socket lock")
}

// TestOwnerSocketBindsReplacesAndConflicts drives the bind path through the
// helper alone (this file never imports "net"): a fresh bind is a 0600
// socket at path, a second bind while it listens is a conflict, Close
// removes it, and a stale hard-linked copy of a closed socket is replaced.
func TestOwnerSocketBindsReplacesAndConflicts(t *testing.T) {
	dir, err := os.MkdirTemp("", "cou")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path, stale := filepath.Join(dir, "d.sock"), filepath.Join(dir, "s.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("fresh bind: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 || ln.Addr().String() != path {
		t.Fatalf("fresh bind: Lstat %v, %v; Addr %v; want a 0600 socket at %s", fi, err, ln.Addr(), path)
	}
	second, err := ListenOwnerSocket(path)
	if second != nil {
		_ = second.Close()
	}
	wantSocketRefusal(t, err, cascade.KindConflict, "a live listener holds its lock")
	if err := os.Link(path, stale); err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, lerr := os.Lstat(path); lerr == nil {
		t.Fatal("Close left the socket at path")
	}
	before, err := os.Lstat(stale)
	if err != nil {
		t.Fatal(err)
	}
	ln, err = ListenOwnerSocket(stale)
	if err != nil {
		t.Fatalf("own stale socket: %v, want it replaced", err)
	}
	defer func() { _ = ln.Close() }()
	if after, lerr := os.Lstat(stale); lerr != nil || os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("stale socket after rebind: %v, %v; want a fresh 0600 inode", after, lerr)
	}
}
