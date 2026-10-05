//go:build !windows

package runtime

// Purpose: the crash-recovery scan's socket branch under the socket
//   lifetime lock (R127): a held lock is a live daemon even when a connect
//   is refused (an injected and a real full backlog), a non-socket, a
//   symlink or a foreign or badly placed socket is refused and left, a
//   stale socket is removed with the lock held for the whole scan, and the
//   lock is released before the daemon's own bind.
// Constraints: untagged and free of the "net" import (internal/build's
//   no-network unit gate): binds go through ListenOwnerSocket or syscall.
//   Short os.MkdirTemp roots (shortLockDir) keep socket names in sun_path.
// SPORT: runtime/recovery (ADD).

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lockScanOpts is the Scan input every case here uses; dial nil means the
// production dialer.
func lockScanOpts(dir, sock string, dial Dialer, bus EventBus) RecoveryOptions {
	return RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  sock,
		Clock:       NewFixedClock(time.Unix(9500, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dial,
		Bus:         bus,
	}
}

// wantLiveDaemon fails unless err is Scan's live-daemon abort.
func wantLiveDaemon(t *testing.T, ev *RecoveryEvent, err error) {
	t.Helper()
	if ev != nil || !errors.Is(err, ErrDaemonAlreadyRunning) || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Scan = (%+v, %v), want the live-daemon conflict and no event", ev, err)
	}
}

// wantKindLeft fails unless err carries kind and path still names before.
func wantKindLeft(t *testing.T, err error, kind cascade.Kind, path string, before os.FileInfo) {
	t.Helper()
	if !cascade.HasKind(err, kind) || errors.Is(err, ErrDaemonAlreadyRunning) {
		t.Fatalf("Scan error = %v, want a %v refusal", err, kind)
	}
	wantSameEntry(t, path, before)
}

// fillBacklog queues non-blocking connects on the listener at path until
// the kernel refuses one (ECONNREFUSED on darwin, EAGAIN on linux): a live
// listener whose backlog is full. The queued descriptors close at cleanup.
func fillBacklog(t *testing.T, path string) error {
	t.Helper()
	var fds []int
	t.Cleanup(func() {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	})
	for i := 0; i < 4096; i++ {
		fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("socket after %d queued connects: %v", i, err)
		}
		fds = append(fds, fd)
		if err := syscall.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
		err = syscall.Connect(fd, &syscall.SockaddrUnix{Name: path})
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
	}
	t.Fatal("the listener's backlog never filled")
	return nil
}

// listenDuringPublish is an EventBus that tries the daemon's own bind from
// inside Scan's publish step and records what the helper said.
type listenDuringPublish struct {
	path string
	err  error
}

func (b *listenDuringPublish) Publish(context.Context, string, string, string, []byte) error {
	ln, err := ListenOwnerSocket(b.path)
	if err == nil {
		_ = ln.Close()
	}
	b.err = err
	return nil
}

// TestRecoveryScanRespectsSocketLock [R127]: the scan removes a socket
// path only with its lifetime lock acquired and held until it returns.
func TestRecoveryScanRespectsSocketLock(t *testing.T) {
	t.Run("held lock with an injected full backlog", scanHeldLockInjected)
	t.Run("held lock with a real full backlog", scanHeldLockRealBacklog)
	t.Run("regular file refused and left", scanRegularFileLeft)
	t.Run("symlink to a dead socket refused and left", scanSymlinkLeft)
	t.Run("writable directory refused and left", scanWritableDirLeft)
	t.Run("foreign or non-socket entry refused", judgeForeignAndNonSocket)
	t.Run("stale after the holder died, lock held to the end", scanStaleLockedThenReleased)
	t.Run("remove failure under the lock propagates", scanRemoveFailureUnderLock)
}

// scanHeldLockInjected: a live listener holds the lock and the dialer says
// ECONNREFUSED, as darwin does for a full backlog; the socket and lock stay.
func scanHeldLockInjected(t *testing.T) {
	dir := shortLockDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	before, lockBefore := mustLstat(t, path), mustLstat(t, path+".lock")
	ev, err := Scan(context.Background(), lockScanOpts(dir, path, dialerStub(false, syscall.ECONNREFUSED), nil))
	wantLiveDaemon(t, ev, err)
	wantSameEntry(t, path, before)
	wantSameEntry(t, path+".lock", lockBefore)
}

// scanHeldLockRealBacklog: the same through the production dialer against
// a listener whose backlog the kernel reports full.
func scanHeldLockRealBacklog(t *testing.T) {
	dir := shortLockDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	t.Logf("backlog full: %v", fillBacklog(t, path))
	before := mustLstat(t, path)
	ev, err := Scan(context.Background(), lockScanOpts(dir, path, nil, nil))
	wantLiveDaemon(t, ev, err)
	wantSameEntry(t, path, before)
}

// scanRegularFileLeft: a regular file is refused (KindConflict) before any
// dial, even with the ECONNREFUSED linux gives for one; it and a dead pid's
// pidfile stay unchanged.
func scanRegularFileLeft(t *testing.T) {
	dir := shortLockDir(t)
	path := filepath.Join(dir, "d.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid := []byte(intToBytes(deadPid(t)))
	if err := os.WriteFile(filepath.Join(dir, "daemon.pid"), pid, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustLstat(t, path)
	_, err := Scan(context.Background(), lockScanOpts(dir, path, dialerStub(false, syscall.ECONNREFUSED), nil))
	wantKindLeft(t, err, cascade.KindConflict, path, before)
	if got, _ := os.ReadFile(path); string(got) != "keep" {
		t.Fatalf("regular file content = %q, want unchanged", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "daemon.pid")); !bytes.Equal(got, pid) {
		t.Fatalf("pidfile = %q, want it untouched by the refused scan", got)
	}
}

// scanSymlinkLeft: a symlink is dialed (R-14.161), and when its target
// refuses the connect it is refused, not removed; the target stays too.
func scanSymlinkLeft(t *testing.T) {
	dir := shortLockDir(t)
	path, target := filepath.Join(dir, "d.sock"), filepath.Join(dir, "t.sock")
	targetFI := rawStaleSocket(t, target)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	before := mustLstat(t, path)
	_, err := Scan(context.Background(), lockScanOpts(dir, path, nil, nil))
	wantKindLeft(t, err, cascade.KindConflict, path, before)
	wantSameEntry(t, target, targetFI)
}

// scanWritableDirLeft: the listener's directory rule applies to the scan:
// a stale socket in a 0777 non-sticky directory is refused and left.
func scanWritableDirLeft(t *testing.T) {
	dir := filepath.Join(shortLockDir(t), "open")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "d.sock")
	before := rawStaleSocket(t, path)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := Scan(context.Background(), lockScanOpts(dir, path, dialerStub(false, syscall.ECONNREFUSED), nil))
	wantKindLeft(t, err, cascade.KindPermissionDenied, path, before)
}

// judgeForeignAndNonSocket: the scan's entry rule refuses a socket another
// uid owns (injected owner) and any non-socket, and accepts our own socket.
func judgeForeignAndNonSocket(t *testing.T) {
	path := filepath.Join(shortLockDir(t), "d.sock")
	fi := rawStaleSocket(t, path)
	euid := os.Geteuid()
	foreign := func(os.FileInfo) (uint32, bool) { return uint32(euid + 1), true }
	if err := judgeRecoveryEntryAs(path, fi, euid, foreign); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("foreign-owned socket: %v, want permission-denied", err)
	}
	if err := judgeRecoveryEntryAs(path, fi, euid, statOwner); err != nil {
		t.Fatalf("our own socket: %v, want accepted", err)
	}
	if err := judgeRecoveryEntry(path, fakeSocketInfo{mode: 0o600}); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("regular-file FileInfo: %v, want conflict", err)
	}
}

// scanStaleLockedThenReleased: a listener that died without Close leaves
// its socket; Scan removes it (StaleSocket) while holding the lock through
// its publish step, then releases it. A second starter that takes the lock
// in the gap before the daemon's own bind wins it, and the late bind and a
// repeated scan both refuse without touching the winner's socket.
func scanStaleLockedThenReleased(t *testing.T) {
	dir := shortLockDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.(*ownerSocketListener) // a crash: fd and lock dropped, no Close
	_, _ = dead.Listener.Close(), dead.lock.Close()
	bus := &listenDuringPublish{path: path}
	ev, err := Scan(context.Background(), lockScanOpts(dir, path, nil, bus))
	if err != nil || ev == nil || !ev.StaleSocket {
		t.Fatalf("Scan = (%+v, %v), want StaleSocket", ev, err)
	}
	if _, lerr := os.Lstat(path); !errors.Is(lerr, os.ErrNotExist) || !cascade.HasKind(bus.err, cascade.KindConflict) {
		t.Fatalf("after Scan: path %v, bind during Scan %v; want removed, and conflict while Scan ran", lerr, bus.err)
	}
	mustLstat(t, path+".lock")
	winner, err := ListenOwnerSocket(path)
	if err != nil {
		t.Fatalf("bind after Scan returned: %v, want the lock released", err)
	}
	defer func() { _ = winner.Close() }()
	won := mustLstat(t, path)
	if _, err := ListenOwnerSocket(path); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("late bind: %v, want conflict", err)
	}
	ev, err = Scan(context.Background(), lockScanOpts(dir, path, nil, nil))
	wantLiveDaemon(t, ev, err)
	wantSameEntry(t, path, won)
}

// scanRemoveFailureUnderLock: with the lock file already there, a
// read-only directory still lets the scan lock but not remove; the error
// propagates and the socket stays.
func scanRemoveFailureUnderLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}
	dir := shortLockDir(t)
	path := filepath.Join(dir, "d.sock")
	before := rawStaleSocket(t, path)
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := Scan(context.Background(), lockScanOpts(dir, path, dialerStub(false, syscall.ECONNREFUSED), nil))
	wantKindLeft(t, err, cascade.KindUnavailable, path, before)
}

// mustLstat returns path's Lstat or fails.
func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
