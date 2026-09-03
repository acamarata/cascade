// Purpose: TestKillNineHarness, the ticket-mandated kill -9 test harness
//   — split from recovery_test.go under R-14.117 (Art.10.3's 300-line
//   cap; a cap-driven split joins the ticket's authorized write set
//   automatically).
// Constraints: Art.7.1 — every path used is under t.TempDir(). No sleeps
//   (R-14.136) — waits on cmd.Wait(), never time.Sleep.

package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestKillNineHarness is the ticket-mandated kill -9 harness: a real
// child process writes a pidfile and a socket-path artifact, is then
// SIGKILLed (not asked to exit cleanly), and Scan is proven to detect
// and clean up both artifacts it left behind, publish a RecoveryEvent,
// and be a no-op on the following, now-clean call.
func TestKillNineHarness(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	sockPath := filepath.Join(dir, "daemon.sock")
	killNineChild(t, pidPath, sockPath)

	bus := &fakeEventBus{}
	opts := RecoveryOptions{
		PidfilePath: pidPath,
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(9000, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		Bus:         bus,
		Dial:        dialerStub(false, syscall.ECONNREFUSED),
	}
	ev, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Scan after kill -9: %v", err)
	}
	if ev == nil || !ev.StalePID || !ev.StaleSocket {
		t.Fatalf("Scan after kill -9: got %+v, want StalePID=true, StaleSocket=true", ev)
	}
	if _, statErr := os.Stat(pidPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("pidfile still present after kill -9 recovery")
	}
	if _, statErr := os.Stat(sockPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("socket file still present after kill -9 recovery")
	}
	if len(bus.published) != 1 {
		t.Fatalf("published %d RecoveryEvents, want 1", len(bus.published))
	}

	// Idempotency: the next Scan call, against the now-clean state, is a
	// pure no-op. The socket file is gone now, so probeSocket takes the
	// "no file" branch and never calls Dial again.
	opts.Clock = NewFixedClock(time.Unix(9001, 0))
	ev2, err2 := Scan(context.Background(), opts)
	if err2 != nil || ev2 != nil {
		t.Fatalf("second Scan after cleanup: got (%+v, %v), want (nil, nil)", ev2, err2)
	}
	if len(bus.published) != 1 {
		t.Fatalf("second Scan published an event on a clean state: %d total", len(bus.published))
	}
}

// killNineChild spawns a real child process that writes pidPath, seeds
// sockPath as its (stale-to-be) socket-path artifact, then SIGKILLs and
// reaps the child — split out of TestKillNineHarness to stay under
// Art.10.3's 50-line function cap (funlen).
func killNineChild(t *testing.T, pidPath, sockPath string) {
	t.Helper()
	// A real child that blocks until killed — long enough that the test
	// can SIGKILL it before it would ever exit on its own.
	cmd := exec.Command("/bin/sh", "-c", "sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(intToBytes(cmd.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	writeSocketPlaceholder(t, sockPath) // the child's socket, as if it had bound and later crashed

	if err := cmd.Process.Kill(); err != nil { // SIGKILL
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait() // reap so the pid is unambiguously ESRCH afterward
}
