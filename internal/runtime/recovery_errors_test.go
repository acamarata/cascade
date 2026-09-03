// Purpose: recovery.go's error-path, PID-recycling, and startup-race
//   coverage — split from recovery_test.go under R-14.117 (Art.10.3's
//   300-line cap; a cap-driven split joins the ticket's authorized write
//   set automatically). Shares this package's fakeEventBus/fakeRegistry/
//   dialerStub/deadPid/writeSocketPlaceholder test doubles, all defined
//   in recovery_test.go per Art.1's "confined to _test.go" rule.
// Constraints: Art.7.1 — every filesystem path used is under t.TempDir().
//   Art.7.2 — no "net"/"net/http" import in this file either; see
//   recovery_test.go's file-level doc comment for the injected-Dialer
//   rationale.

package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestRecoveryScanStaleAdvisoryLock_LiveOwnerSkipped proves the same
// PID-recycling defense applies to registry locks: an OrphanedLocks
// candidate whose owner pid is still alive is never released.
func TestRecoveryScanStaleAdvisoryLock_LiveOwnerSkipped(t *testing.T) {
	dir := t.TempDir()
	reg := &fakeRegistry{locks: []OrphanedLock{{LockID: "sched:lock", OwnerPID: os.Getpid()}}}

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(4100, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev != nil {
		t.Fatalf("Scan: got %+v, want nil (live owner must never be released)", ev)
	}
	if len(reg.released) != 0 {
		t.Fatalf("registry.released = %v, want none", reg.released)
	}
}

// TestRecoveryScanStartupRace is the STARTUP-RACE proof: when the socket
// probe finds a live listener, Scan must not have touched the pidfile at
// all — not read past ReadPidfile, not removed it — because a
// concurrently-starting daemon may have just written and be actively
// using it. This asserts the pidfile's bytes and the socket-path
// artifact are byte-for-byte/still-present unchanged after the aborted
// Scan.
func TestRecoveryScanStartupRace(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	writeSocketPlaceholder(t, sockPath)

	pidPath := filepath.Join(dir, "daemon.pid")
	// A pid a concurrently-starting daemon plausibly just wrote: our own
	// pid, very much alive, so this also proves the abort precedes any
	// liveness check at all — Scan must never even ask.
	original := []byte(intToBytes(os.Getpid()))
	if err := os.WriteFile(pidPath, original, 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	_, scanErr := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(5100, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dialerStub(true, nil),
	})
	if scanErr == nil {
		t.Fatal("Scan against a live socket: want error, got nil")
	}

	after, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("pidfile disappeared after aborted Scan: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("pidfile mutated by an aborted Scan: got %q, want unchanged %q", after, original)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket-path artifact disappeared after aborted Scan: %v", err)
	}
}

func TestRecoveryScan_PidfileParseErrorSkipsRemoval(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("not-an-integer"), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	var buf bytes.Buffer
	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(6000, 0)),
		Log:         testLogger(&buf),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev != nil {
		t.Fatalf("Scan: got %+v, want nil (unparsable pidfile is left alone, not treated as stale)", ev)
	}
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("unparsable pidfile was removed: %v", statErr)
	}
	if !bytes.Contains(buf.Bytes(), []byte("unparsable")) {
		t.Fatalf("expected a WARN log mentioning the unparsable pidfile, got: %s", buf.String())
	}
}

func TestRecoveryScan_SocketRemoveErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	writeSocketPlaceholder(t, sockPath)

	// Make the containing directory unwritable so os.Remove fails with a
	// permission error instead of succeeding.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}

	_, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(7000, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dialerStub(false, syscall.ECONNREFUSED),
	})
	if err == nil {
		t.Fatal("Scan with an unremovable stale socket: want error, got nil")
	}
}

func TestRecoveryScan_RegistryReleaseErrorLoggedAndContinues(t *testing.T) {
	dir := t.TempDir()
	pidA := deadPid(t)
	pidB := deadPid(t)
	reg := &fakeRegistry{
		locks: []OrphanedLock{
			{LockID: "lock-a", OwnerPID: pidA},
			{LockID: "lock-b", OwnerPID: pidB},
		},
		releaseErr: map[string]error{"lock-a": errors.New("release: boom")},
	}
	var buf bytes.Buffer
	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(8000, 0)),
		Log:         testLogger(&buf),
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev == nil || len(ev.StaleLocks) != 1 || ev.StaleLocks[0] != "lock-b" {
		t.Fatalf("Scan: got %+v, want StaleLocks=[lock-b] (lock-a failed, lock-b still processed)", ev)
	}
	if !bytes.Contains(buf.Bytes(), []byte("lock-a")) {
		t.Fatalf("expected a WARN log mentioning the failed release of lock-a, got: %s", buf.String())
	}
}

// TestRecoveryScan_RegistryListErrorSkipsLockStep proves an
// OrphanedLocks failure aborts only the lock-scanning phase — pidfile
// cleanup, which already happened independently, still shows up in the
// resulting event.
func TestRecoveryScan_RegistryListErrorSkipsLockStep(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	pid := deadPid(t)
	if err := os.WriteFile(pidPath, []byte(intToBytes(pid)), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	reg := &fakeRegistry{listErr: errors.New("registry: list: boom")}

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(8100, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev == nil || !ev.StalePID || len(ev.StaleLocks) != 0 {
		t.Fatalf("Scan: got %+v, want StalePID=true, StaleLocks=[]", ev)
	}
}
