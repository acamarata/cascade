// Purpose: coverage for Scan's most safety-critical fail-safe branches
//   — the ones a CR of this ticket found untested: the PID-recycling
//   defense (scanPidfile's `liveness != ProcessLivenessDead` skip
//   branch), probeSocket's undecidable-dial-error branch and its
//   propagation through Scan, and the two remaining untested I/O-failure
//   paths (RemovePidfile failure, EventBus.Publish failure). Split from
//   recovery_errors_test.go under R-14.117 (Art.10.3's 300-line cap; a
//   cap-driven split joins the ticket's authorized write set
//   automatically — recovery_errors_test.go was already at 205 lines
//   before these four tests). Shares this package's fakeEventBus/
//   fakeRegistry/dialerStub/deadPid/writeSocketPlaceholder test doubles,
//   all defined in recovery_test.go/recovery_testutil_test.go per Art.1's
//   "confined to _test.go" rule.
// Constraints: Art.7.1 — every filesystem path used is under t.TempDir().
//   Art.7.2 — no "net"/"net/http" import in this file; see
//   recovery_test.go's file-level doc comment for the injected-Dialer
//   rationale.

package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecoveryScanPidfile_LiveDifferentProcessNeverTouched is the
// PID-recycling defense proof: a pidfile naming a pid the OS reports as
// currently alive — this test process's own pid, the obvious live pid
// to use — must be left completely untouched, and the scan must report
// it as skipped rather than cleaned. Unlike
// TestRecoveryScanStartupRace/TestRecoveryScanLiveDaemonBlocked (which
// prove the pidfile is never even READ because the socket probe aborts
// Scan first), this test leaves the socket clean so execution actually
// reaches scanPidfile's liveness check — the branch a CR of this ticket
// found at 0/2 coverage.
func TestRecoveryScanPidfile_LiveDifferentProcessNeverTouched(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	original := []byte(intToBytes(os.Getpid()))
	if err := os.WriteFile(pidPath, original, 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	var buf bytes.Buffer
	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(9200, 0)),
		Log:         testLogger(&buf),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev != nil {
		t.Fatalf("Scan: got %+v, want nil (a pidfile naming a live pid is not stale state)", ev)
	}

	after, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("pidfile disappeared: %v", readErr)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("pidfile mutated: got %q, want unchanged %q", after, original)
	}
	if !bytes.Contains(buf.Bytes(), []byte("skipping removal")) {
		t.Fatalf("expected a WARN log reporting the pidfile as skipped, got: %s", buf.String())
	}
}

// TestRecoveryScan_SocketProbeUndecidablePropagates proves the
// undecidable-dial-error branch: a dial failure that is neither success
// nor ECONNREFUSED (a permission error, here) cannot be classified as
// live or confirmed-stale, so probeSocket returns an error, Scan
// propagates it without constructing an event, and — critically — never
// touches the socket or pidfile artifacts, per the same "when in doubt,
// do not clean" contract the PID-recycling defense rests on.
func TestRecoveryScan_SocketProbeUndecidablePropagates(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	writeSocketPlaceholder(t, sockPath)
	pidPath := filepath.Join(dir, "daemon.pid")
	originalPid := []byte(intToBytes(deadPid(t)))
	if err := os.WriteFile(pidPath, originalPid, 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	undecidable := errors.New("dial unix: permission denied")
	_, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(9300, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dialerStub(false, undecidable),
	})
	if err == nil {
		t.Fatal("Scan with an undecidable socket probe: want error, got nil")
	}
	if !errors.Is(err, undecidable) {
		t.Fatalf("Scan error = %v, want it to wrap %v", err, undecidable)
	}

	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Fatalf("socket artifact disappeared after an undecidable probe: %v", statErr)
	}
	afterPid, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("pidfile disappeared after an undecidable probe: %v", readErr)
	}
	if !bytes.Equal(afterPid, originalPid) {
		t.Fatalf("pidfile mutated by an aborted (undecidable) Scan: got %q, want unchanged %q", afterPid, originalPid)
	}
}

// TestRecoveryScan_PidfileRemoveErrorLoggedAndSkipped covers
// scanPidfile's RemovePidfile-failure branch: a confirmed-stale pidfile
// that cannot actually be removed (its containing directory is made
// read-only here) is logged and left in place — reported, not silently
// dropped, and not escalated to a Scan-aborting error either.
func TestRecoveryScan_PidfileRemoveErrorLoggedAndSkipped(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	pid := deadPid(t)
	if err := os.WriteFile(pidPath, []byte(intToBytes(pid)), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}

	var buf bytes.Buffer
	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(9400, 0)),
		Log:         testLogger(&buf),
	})
	if err != nil {
		t.Fatalf("Scan: %v (a stale-but-unremovable pidfile is logged, not a Scan-aborting error)", err)
	}
	if ev != nil {
		t.Fatalf("Scan: got %+v, want nil (removal failed, so StalePID was never set)", ev)
	}
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("pidfile disappeared despite an unwritable directory: %v", statErr)
	}
	if !bytes.Contains(buf.Bytes(), []byte("failed to remove confirmed-stale pidfile")) {
		t.Fatalf("expected a WARN log about the failed removal, got: %s", buf.String())
	}
}

// TestRecoveryScan_BusPublishErrorPropagates covers Scan's
// Bus.Publish-failure branch: when a RecoveryEvent WAS produced (a stale
// pidfile here) but the injected EventBus fails to publish it, Scan
// returns both the event (already-performed cleanup is not undone) and
// a wrapped error, rather than silently swallowing the publish failure.
func TestRecoveryScan_BusPublishErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	pid := deadPid(t)
	if err := os.WriteFile(pidPath, []byte(intToBytes(pid)), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	publishErr := errors.New("bus: publish: boom")
	bus := &fakeEventBus{publishErr: publishErr}

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(9500, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Bus:         bus,
	})
	if err == nil {
		t.Fatal("Scan with a failing EventBus.Publish: want error, got nil")
	}
	if !errors.Is(err, publishErr) {
		t.Fatalf("Scan error = %v, want it to wrap %v", err, publishErr)
	}
	if ev == nil || !ev.StalePID {
		t.Fatalf("Scan: got %+v, want the RecoveryEvent still returned (cleanup already happened) with StalePID=true", ev)
	}
	if len(bus.published) != 0 {
		t.Fatalf("bus.published = %v, want none (Publish itself failed)", bus.published)
	}
}
