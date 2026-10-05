//go:build !windows

package daemon

// Purpose: lifecycle_unix.go's socket-level coverage: drain's log lines,
//   setUpSocketAndPIDFile's prepare/pidfile/publish order (R130) and failure
//   paths, and runtime.ListenOwnerSocket as the daemon uses it (success, live
//   conflict, stale socket rebound, regular file refused and left, unusable
//   parent). No "net" import (raw syscall sockets only), so Art.7.2's gate
//   does not apply. Run's lifecycle tests live in daemon_run_test.go.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestIsAddrInUse covers the address-in-use handling that moved from this
// package into runtime.ListenOwnerSocket: a path a live daemon socket is
// listening on is a KindConflict, and that socket keeps its inode and its
// 0600 mode.
func TestIsAddrInUse(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	live, err := runtime.ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("listenSocket: %v", err)
	}
	defer func() { _ = live.Close() }()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.ListenOwnerSocket(path)
	if second != nil {
		_ = second.Close()
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict || !strings.Contains(err.Error(), "a live listener holds its lock") {
		t.Fatalf("listenSocket on a live socket: %v, want a KindConflict naming the live listener", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("live socket after the conflict: %v, %v; want the same 0600 inode", after, err)
	}
}

func TestDrain_LogsStartAndEndWithConnectionCount(t *testing.T) {
	log, records := newRecordingLogger()
	var active int64 = 3
	drain(RunOptions{Logger: log, Settings: Settings{ShutdownGrace: 2 * time.Second}}, &active)
	if len(*records) != 2 {
		t.Fatalf("records = %+v, want exactly 2 (drain start, drain end)", *records)
	}
	if (*records)[0].Message != "daemon: drain start" || (*records)[1].Message != "daemon: drain end" {
		t.Errorf("messages = %q, %q", (*records)[0].Message, (*records)[1].Message)
	}
	for _, r := range *records {
		var sawConnections bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "connections" && a.Value.Int64() == 3 {
				sawConnections = true
			}
			return true
		})
		if !sawConnections {
			t.Errorf("record %q missing connections=3 attribute", r.Message)
		}
	}
}

func TestDrain_NilLoggerIsNoop(t *testing.T) {
	// Explicit assertion: the contract is "does nothing, does not panic".
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("drain with a nil logger panicked: %v", r)
		}
	}()
	var active int64
	drain(RunOptions{}, &active)
}

func TestListenSocket_Success_BindsWith0600Perms(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := runtime.ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("listenSocket: %v", err)
	}
	defer func() { _ = ln.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %v, want 0600", perm)
	}
}

// rawStaleSocket leaves a real socket file at path that nobody listens on:
// a descriptor bound there and closed without listen, as a crashed daemon
// leaves it.
func rawStaleSocket(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	_ = syscall.Close(fd)
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket at %s: %v, %v", path, fi, err)
	}
	return fi
}

// TestListenSocket_StaleSocketFile_RemovedAndRebound: a real socket file a
// crashed prior daemon left (nobody listening, connect refused) is our own
// stale socket, so it is removed and the bind retried, never a failure.
func TestListenSocket_StaleSocketFile_RemovedAndRebound(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	before := rawStaleSocket(t, path)
	ln, err := runtime.ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("listenSocket against a stale socket file: %v", err)
	}
	defer func() { _ = ln.Close() }()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || os.SameFile(before, info) {
		t.Errorf("after rebind %v is not a fresh socket", info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms after rebind = %v, want 0600", perm)
	}
}

// TestListenSocket_RegularFile_RefusedAndLeft: a regular file at the socket
// path is not proven to be our stale socket, so it is refused with
// KindPermissionDenied and left byte-for-byte in place.
func TestListenSocket_RegularFile_RefusedAndLeft(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := runtime.ListenOwnerSocket(path)
	if ln != nil {
		_ = ln.Close()
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied || !strings.Contains(err.Error(), "is not a socket") {
		t.Fatalf("listenSocket against a regular file: %v, want KindPermissionDenied", err)
	}
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "not a socket" {
		t.Fatalf("regular file after refusal = %q, %v; want it unchanged", got, rerr)
	}
}

func TestListenSocket_UnusableParentDirIsError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ListenOwnerSocket(filepath.Join(blocker, "d.sock")); err == nil {
		t.Fatal("listenSocket under a file (not a dir) succeeded, want an error")
	}
}

// TestSetUpSocket_HelperKindsReachCallerAndCleanupLeavesSuccessor: the
// helper's permission-denied and conflict kinds reach setUpSocketAndPIDFile's
// caller unchanged (no pidfile is left on either failure), and cleanup
// closes the listener without removing a socket this run did not bind.
func TestSetUpSocket_HelperKindsReachCallerAndCleanupLeavesSuccessor(t *testing.T) {
	dir := shortTempDir(t)
	sock, pid := filepath.Join(dir, "d.sock"), filepath.Join(dir, "d.pid")
	wantKind := func(sock, pid string, kind cascade.Kind) {
		t.Helper()
		if _, err := setUpAt(sock, pid); !cascade.HasKind(err, kind) {
			t.Fatalf("setUpSocketAndPIDFile(%s): %v, want kind %v", sock, err, kind)
		}
		if _, serr := os.Stat(pid); serr == nil {
			t.Fatalf("pidfile %s left after a failed socket setup", pid)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "r.sock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantKind(filepath.Join(dir, "r.sock"), filepath.Join(dir, "r.pid"), cascade.KindPermissionDenied)
	cleanup, err := setUpAt(sock, pid)
	if err != nil {
		t.Fatalf("setUpSocketAndPIDFile: %v", err)
	}
	wantKind(sock, filepath.Join(dir, "d2.pid"), cascade.KindConflict)
	// A successor's socket now sits at the path; cleanup must leave it.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	successor := rawStaleSocket(t, sock)
	cleanup()
	if after, lerr := os.Lstat(sock); lerr != nil || !os.SameFile(successor, after) {
		t.Fatalf("socket after cleanup: %v, %v; want the successor's socket left", after, lerr)
	}
	if _, serr := os.Stat(pid); serr == nil {
		t.Fatal("cleanup left the pidfile")
	}
}

// setUpAt runs setUpSocketAndPIDFile for one socket and pidfile path.
func setUpAt(sock, pid string) (func(), error) {
	clock := runtime.NewSystemClock()
	_, cleanup, err := setUpSocketAndPIDFile(RunOptions{Settings: Settings{SocketPath: sock}, PIDPath: pid, Clock: clock}, NewManifest(nil, clock))
	return cleanup, err
}

// TestConcurrentDaemonStartKeepsWinnerPidfile (R129, R130): two setups on
// one path at once leave one listener and a pidfile naming us, the loser
// gets KindConflict, a later loser replaces neither file, and nothing is at
// the path (nothing can dial it) until the pidfile is written.
func TestConcurrentDaemonStartKeepsWinnerPidfile(t *testing.T) {
	dir := shortTempDir(t)
	sock, pid := filepath.Join(dir, "d.sock"), filepath.Join(dir, "d.pid")
	t.Cleanup(func() { setUpStageHook = func(string) {} })
	setUpStageHook = func(string) {
		if _, err := os.Lstat(sock); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("socket path present before the pidfile write: %v", err)
		}
	}
	errs, cleanups := make([]error, 2), make([]func(), 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { cleanups[i], errs[i] = setUpAt(sock, pid) })
	}
	wg.Wait()
	won := slices.IndexFunc(errs, func(e error) bool { return e == nil })
	if won < 0 || !cascade.HasKind(errs[1-won], cascade.KindConflict) {
		t.Fatalf("concurrent setups returned %v; want one listener and one KindConflict", errs)
	}
	t.Cleanup(cleanups[won])
	files := func() (os.FileInfo, os.FileInfo) {
		s, serr := os.Lstat(sock)
		p, perr := os.Lstat(pid)
		if rec, ok, err := readPIDFile(pid); serr != nil || perr != nil || !ok || err != nil || rec.PID != os.Getpid() {
			t.Fatalf("socket %v, pidfile %v, record %+v %v %v; want both, naming this process", serr, perr, rec, ok, err)
		}
		return s, p
	}
	s1, p1 := files()
	if _, err := setUpAt(sock, pid); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("later setup: %v, want KindConflict", err)
	}
	if s2, p2 := files(); !os.SameFile(s1, s2) || !os.SameFile(p1, p2) || !p1.ModTime().Equal(p2.ModTime()) {
		t.Fatal("a losing setup replaced the winner's socket or pidfile")
	}
}

// TestSetUpSocket_FailureAfterPrepareReleasesLock: a failed pidfile write (a
// directory there) and a failed publish (a file planted at the socket path)
// each drop the private bind directory and release the lock; the failed
// publish removes its own pidfile and leaves the planted file.
func TestSetUpSocket_FailureAfterPrepareReleasesLock(t *testing.T) {
	dir := shortTempDir(t)
	sock, pid := filepath.Join(dir, "d.sock"), filepath.Join(dir, "d.pid")
	failsAndReleases := func(pidPath, left string) {
		t.Helper()
		_, err := setUpAt(sock, pidPath)
		got, _ := os.ReadFile(sock)
		entries, _ := os.ReadDir(dir)
		if _, serr := os.Stat(filepath.Join(pid, "p")); err == nil || string(got) != left || serr == nil || strings.HasPrefix(entries[0].Name(), ".") {
			t.Fatalf("setup %v; socket path %q (want %q), own pidfile %v, directory %v", err, got, left, serr, entries)
		}
		_ = os.Remove(sock)
		ln, lerr := runtime.ListenOwnerSocket(sock)
		if lerr != nil {
			t.Fatalf("rebind: %v, want the lock released", lerr)
		}
		_ = ln.Close()
	}
	if err := os.Mkdir(pid, 0o700); err != nil {
		t.Fatal(err)
	}
	failsAndReleases(pid, "")
	t.Cleanup(func() { setUpStageHook = func(string) {} })
	setUpStageHook = func(string) { _ = os.WriteFile(sock, []byte("planted"), 0o600) }
	failsAndReleases(filepath.Join(pid, "p"), "planted")
}
