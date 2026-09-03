// Purpose: recovery.go's Scan coverage for five of the six ticket-
//   mandated named scenarios (clean state, stale pidfile, stale socket,
//   stale advisory lock, live-daemon-blocked). The sixth
//   (TestKillNineHarness) lives in recovery_killnine_test.go, shared test
//   doubles live in recovery_testutil_test.go, and error-path/PID-
//   recycling/startup-race coverage lives in recovery_errors_test.go —
//   all split under R-14.117 (Art.10.3's 300-line cap; a cap-driven split
//   joins the ticket's authorized write set automatically).
// Constraints: Art.7.1 — every filesystem path used is under t.TempDir().
//   Art.7.2 — this file (and every recovery*_test.go sibling except the
//   `//go:build integration`-tagged recovery_integration_test.go) imports
//   neither "net" nor "net/http": the socket probe is exercised entirely
//   through the injected Dialer seam (recovery.go's Dialer doc comment
//   explains why), never real network I/O.

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRecoveryScanCleanState(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	opts := RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(1000, 0)),
		Log:         testLogger(&buf),
	}
	ev, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Scan on clean state: %v", err)
	}
	if ev != nil {
		t.Fatalf("Scan on clean state: got event %+v, want nil (no-event = clean startup)", ev)
	}

	// Idempotency: a second call is still a clean no-op.
	ev2, err2 := Scan(context.Background(), opts)
	if err2 != nil || ev2 != nil {
		t.Fatalf("second Scan on clean state: got (%+v, %v), want (nil, nil)", ev2, err2)
	}
}

func TestRecoveryScanStalePidfile(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	pid := deadPid(t)
	if err := os.WriteFile(pidPath, []byte(intToBytes(pid)), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	bus := &fakeEventBus{}
	fixed := time.Unix(2000, 0)
	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(fixed),
		Log:         testLogger(&bytes.Buffer{}),
		Bus:         bus,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev == nil || !ev.StalePID {
		t.Fatalf("Scan: got %+v, want StalePID=true", ev)
	}
	if !ev.RecoveredAt.Equal(fixed) {
		t.Fatalf("RecoveredAt = %v, want %v (injected clock)", ev.RecoveredAt, fixed)
	}
	if _, statErr := os.Stat(pidPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("pidfile still present after stale-pid cleanup")
	}
	if len(bus.published) != 1 {
		t.Fatalf("published %d events, want 1", len(bus.published))
	}
	var decoded RecoveryEvent
	if err := json.Unmarshal(bus.published[0].payload, &decoded); err != nil {
		t.Fatalf("decode published payload: %v", err)
	}
	if !decoded.StalePID {
		t.Fatalf("published event StalePID = false, want true")
	}
}

func TestRecoveryScanStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	writeSocketPlaceholder(t, sockPath)

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(3000, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dialerStub(false, syscall.ECONNREFUSED),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev == nil || !ev.StaleSocket {
		t.Fatalf("Scan: got %+v, want StaleSocket=true", ev)
	}
	if _, statErr := os.Stat(sockPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("socket file still present after stale-socket cleanup")
	}
}

func TestRecoveryScanStaleAdvisoryLock(t *testing.T) {
	dir := t.TempDir()
	pid := deadPid(t)
	reg := &fakeRegistry{locks: []OrphanedLock{{LockID: "sched:lock", OwnerPID: pid}}}

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  filepath.Join(dir, "daemon.sock"),
		Clock:       NewFixedClock(time.Unix(4000, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ev == nil || len(ev.StaleLocks) != 1 || ev.StaleLocks[0] != "sched:lock" {
		t.Fatalf("Scan: got %+v, want StaleLocks=[sched:lock]", ev)
	}
	if len(reg.released) != 1 || reg.released[0] != "sched:lock" {
		t.Fatalf("registry.released = %v, want [sched:lock]", reg.released)
	}
}

func TestRecoveryScanLiveDaemonBlocked(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	writeSocketPlaceholder(t, sockPath)

	// A pidfile naming a dead pid is ALSO present, to prove the abort
	// happens before step 2 ever runs (see TestRecoveryScanStartupRace
	// in recovery_errors_test.go for the explicit "nothing was touched"
	// assertion).
	pidPath := filepath.Join(dir, "daemon.pid")
	pid := deadPid(t)
	if err := os.WriteFile(pidPath, []byte(intToBytes(pid)), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	ev, err := Scan(context.Background(), RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(5000, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Dial:        dialerStub(true, nil),
	})
	if err == nil {
		t.Fatal("Scan against a live socket: want error, got nil")
	}
	if !errors.Is(err, ErrDaemonAlreadyRunning) {
		t.Fatalf("Scan error = %v, want errors.Is(err, ErrDaemonAlreadyRunning)", err)
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("Scan error kind: want KindConflict, got %v", err)
	}
	if ev != nil {
		t.Fatalf("Scan against a live socket: got event %+v, want nil (no cleanup)", ev)
	}
}
