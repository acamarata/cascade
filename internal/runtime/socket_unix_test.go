//go:build !windows && integration

package runtime

// Purpose: ListenOwnerSocket against real bound unix sockets: symlinks,
//   a foreign-owned stale socket, the directory modes, our own stale socket
//   versus a live one, the check-to-link race and the bind-to-chmod window.
// Constraints: binds real sockets, so integration-tagged. Socket paths live
//   under a short os.MkdirTemp root, since t.TempDir() can overflow darwin's
//   104-byte sun_path. The foreign uid is an injected SocketOwner: no chown,
//   no root. HOME is never touched.
// SPORT: internal/runtime (ADD).

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// shortSocketDir returns a fresh 0700 directory with a short path.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cos")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// liveSocket listens at path until the test ends.
func liveSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

// staleSocket leaves a socket file at path that nobody listens on and
// returns its Lstat result.
func staleSocket(t *testing.T, path string) os.FileInfo {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket at %s: %v %v", path, fi, err)
	}
	return fi
}

// wantAnswers fails unless a connect to path succeeds.
func wantAnswers(t *testing.T, path string) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v, want a live listener", path, err)
	}
	_ = c.Close()
}

// wantOwnSocket0600 fails unless path is a socket with mode exactly 0600.
func wantOwnSocket0600(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("Lstat(%s) = %v, %v; want a 0600 socket", path, fi, err)
	}
	return fi
}

func TestListenOwnerSocketRefusesSymlink(t *testing.T) {
	dir := shortSocketDir(t)
	live := filepath.Join(dir, "live.sock")
	liveSocket(t, live)
	for name, target := range map[string]string{"dangling": filepath.Join(dir, "missing"), "to-live": live} {
		path := filepath.Join(dir, name+".sock")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		ln, err := ListenOwnerSocket(path)
		if ln != nil {
			_ = ln.Close()
		}
		wantSocketRefusal(t, err, cascade.KindPermissionDenied, "is a symlink")
		fi, lerr := os.Lstat(path)
		got, rerr := os.Readlink(path)
		if lerr != nil || fi.Mode()&os.ModeSymlink == 0 || rerr != nil || got != target {
			t.Fatalf("%s: after refusal Lstat=%v,%v Readlink=%q,%v; want the symlink to %q", name, fi, lerr, got, rerr, target)
		}
	}
	wantAnswers(t, live)
}

func TestListenOwnerSocketRefusesForeignOwner(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "d.sock")
	before := staleSocket(t, path)
	euid := os.Geteuid()
	foreign := func(fi os.FileInfo) (uint32, bool) {
		if fi.Mode()&os.ModeSocket != 0 {
			return uint32(euid + 1), true
		}
		return statOwner(fi)
	}
	ln, err := listenOwnerSocket(path, euid, foreign)
	if ln != nil {
		_ = ln.Close()
	}
	wantSocketRefusal(t, err, cascade.KindPermissionDenied, "owned by another user")
	if after, lerr := os.Lstat(path); lerr != nil || !os.SameFile(before, after) {
		t.Fatalf("foreign stale socket after refusal: %v, %v; want the same inode left", after, lerr)
	}
}

func TestListenOwnerSocketRefusesWritableDir(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o770} {
		dir := shortSocketDir(t)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		ln, err := ListenOwnerSocket(filepath.Join(dir, "d.sock"))
		if ln != nil {
			_ = ln.Close()
		}
		wantSocketRefusal(t, err, cascade.KindPermissionDenied, "group/other-writable without the sticky bit")
		if entries, rerr := os.ReadDir(dir); rerr != nil || len(entries) != 0 {
			t.Fatalf("mode %o: directory holds %v (%v) after refusal, want no socket file", mode, entries, rerr)
		}
	}
	dir := shortSocketDir(t)
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "d.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("sticky 1777 directory: %v, want a bind", err)
	}
	defer func() { _ = ln.Close() }()
	wantOwnSocket0600(t, path)
	wantAnswers(t, path)
}

func TestListenOwnerSocketRemovesOwnStaleSocket(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "d.sock")
	before := staleSocket(t, path)
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("own stale socket: %v, want it replaced", err)
	}
	defer func() { _ = ln.Close() }()
	if after := wantOwnSocket0600(t, path); os.SameFile(before, after) {
		t.Fatal("the stale inode is still in place, want a fresh socket")
	}
	wantAnswers(t, path)

	livePath := filepath.Join(dir, "live.sock")
	liveSocket(t, livePath)
	liveBefore, _ := os.Lstat(livePath)
	second, err := ListenOwnerSocket(livePath)
	if second != nil {
		_ = second.Close()
	}
	wantSocketRefusal(t, err, cascade.KindConflict, "a live listener answers")
	if after, lerr := os.Lstat(livePath); lerr != nil || !os.SameFile(liveBefore, after) {
		t.Fatalf("live socket after conflict: %v, %v; want it untouched", after, lerr)
	}
	wantAnswers(t, livePath)
}

// dialOnce connects to path once and returns the connect error.
func dialOnce(path string) error {
	c, err := net.Dial("unix", path)
	if err == nil {
		_ = c.Close()
	}
	return err
}

// TestOwnerSocketBindWindowRefusesConnect proves the bind-to-chmod window
// is unreachable: at the "chmod" stage the inode carries its umask-derived
// mode, but it lives under a private 0700 name, nothing exists at path yet,
// and listen(2) has not run, so a connect is refused. At the "listening"
// stage, before the link, the private name already answers: path never
// names a socket that is not listening.
func TestOwnerSocketBindWindowRefusesConnect(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "d.sock")
	var dialErr, pathErr error
	var privMode os.FileMode
	dialed := false
	prev, listenErr := socketStageHook, errors.New("listening stage not reached")
	socketStageHook = func(stage, p string) {
		if stage == "listening" {
			listenErr = dialOnce(p)
		}
		if stage != "chmod" {
			return
		}
		dialed = true
		if fi, err := os.Stat(filepath.Dir(p)); err == nil {
			privMode = fi.Mode().Perm()
		}
		_, pathErr = os.Lstat(path)
		dialErr = dialOnce(p)
	}
	t.Cleanup(func() { socketStageHook = prev })
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if !dialed || !errors.Is(dialErr, syscall.ECONNREFUSED) {
		t.Fatalf("connect inside the bind-to-chmod window: ran=%v err=%v, want ECONNREFUSED", dialed, dialErr)
	}
	if listenErr != nil {
		t.Fatalf("connect to the private name before the link: %v, want an answer", listenErr)
	}
	if privMode != 0o700 || !errors.Is(pathErr, os.ErrNotExist) {
		t.Fatalf("window: private dir mode %o, path Lstat %v; want 0700 and nothing at path", privMode, pathErr)
	}
	wantOwnSocket0600(t, path)
	wantAnswers(t, path)
	if ln.Addr().String() != path {
		t.Fatalf("Addr = %q, want %q", ln.Addr(), path)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 2 {
		t.Fatalf("socket directory holds %v, want only the socket and its lock (private dir removed)", entries)
	}
	_ = ln.Close()
	if _, lerr := os.Lstat(path); !errors.Is(lerr, os.ErrNotExist) {
		t.Fatalf("after Close Lstat = %v, want the socket removed", lerr)
	}
}

// TestOwnerSocketLinkRaceReclassifies plants an entry between the first
// check and the link into place: link reports EEXIST and the entry is
// judged again by the same rules, so a planted symlink is refused and left,
// never followed.
func TestOwnerSocketLinkRaceReclassifies(t *testing.T) {
	dir := shortSocketDir(t)
	path, target := filepath.Join(dir, "d.sock"), filepath.Join(dir, "elsewhere")
	prev := socketStageHook
	t.Cleanup(func() { socketStageHook = prev })
	socketStageHook = func(stage, p string) {
		if stage == "link" {
			_ = os.Symlink(target, p)
		}
	}
	ln, err := ListenOwnerSocket(path)
	if ln != nil {
		_ = ln.Close()
	}
	wantSocketRefusal(t, err, cascade.KindPermissionDenied, "is a symlink")
	if got, rerr := os.Readlink(path); rerr != nil || got != target {
		t.Fatalf("planted symlink after refusal -> %q (%v), want %q", got, rerr, target)
	}
	if _, serr := os.Lstat(target); serr == nil {
		t.Fatal("the planted symlink was followed")
	}

	// Our own stale socket appearing in the same race is removed and the
	// link retried once.
	racePath := filepath.Join(dir, "r.sock")
	socketStageHook = func(stage, p string) {
		if stage == "link" {
			staleSocket(t, p)
		}
	}
	ln, err = ListenOwnerSocket(racePath)
	if err != nil {
		t.Fatalf("own stale socket planted before link: %v, want a relink", err)
	}
	defer func() { _ = ln.Close() }()
	wantOwnSocket0600(t, racePath)
	wantAnswers(t, racePath)
}
