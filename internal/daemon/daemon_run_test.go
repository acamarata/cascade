//go:build !windows

package daemon

// Purpose: Run's full success-to-clean-shutdown lifecycle, driven the
//   same way daemon_unix_integration_test.go's `-tags=integration`
//   sibling does, minus the one assertion (net.Dial-ing the live socket)
//   that actually requires a "net" import — this file instead confirms
//   the socket's liveness via its filesystem side effects (pidfile
//   written, socket file present at 0600, both removed after a clean
//   shutdown) and the drain log lines, which together prove the same real
//   behaviour without the forbidden import (Art.7.2). Also the ctx.Done()
//   shutdown branch (as distinct from the Signals branch) and the nil-
//   Manifest construction branch. Split from daemon_socket_test.go purely
//   for Art.10.3's 300-line file cap.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestRun_FullLifecycle_SignalDrain_NoNetworkImport drives Run end-to-end
// exactly like daemon_unix_integration_test.go's `-tags=integration`
// sibling, EXCEPT it never dials the socket itself.
// runHarness is the shared fixture for the lifecycle tests, split out to keep
// each test function under the 50-line cap (Art.10.3).
type runHarness struct {
	socketPath string
	pidPath    string
	log        *slog.Logger
	records    *[]slog.Record
	signals    chan os.Signal
	ready      chan struct{}
	done       chan error
}

func newRunHarness(t *testing.T) *runHarness {
	t.Helper()
	dir := shortTempDir(t)
	log, records := newRecordingLogger()
	return &runHarness{
		socketPath: filepath.Join(dir, "d.sock"),
		pidPath:    filepath.Join(t.TempDir(), "daemon.pid"),
		log:        log,
		records:    records,
		signals:    make(chan os.Signal, 1),
		ready:      make(chan struct{}),
		done:       make(chan error, 1),
	}
}

func TestRun_FullLifecycle_SignalDrain_NoNetworkImport(t *testing.T) {
	h := newRunHarness(t)
	socketPath, pidPath := h.socketPath, h.pidPath
	log, records := h.log, h.records
	signals, ready, done := h.signals, h.ready, h.done

	go func() {
		done <- Run(context.Background(), RunOptions{
			Settings: Settings{SocketPath: socketPath, ShutdownGrace: 0},
			PIDPath:  pidPath,
			Logger:   log,
			Clock:    runtime.NewSystemClock(),
			Signals:  signals,
			Ready:    ready,
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	assertRunningState(t, pidPath, socketPath)
	signals <- os.Interrupt

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after a termination signal — daemon never drained")
	}

	if _, ok, _ := readPIDFile(pidPath); ok {
		t.Error("pidfile still present after clean shutdown")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file still present after clean shutdown")
	}

	assertDrainLogged(t, records)
}

// assertDrainLogged proves the drain emitted both of its bracketing log
// lines. Extracted to keep the lifecycle test under the 50-line cap.
func assertDrainLogged(t *testing.T, records *[]slog.Record) {
	t.Helper()
	var sawDrainStart, sawDrainEnd bool
	for _, r := range *records {
		switch r.Message {
		case "daemon: drain start":
			sawDrainStart = true
		case "daemon: drain end":
			sawDrainEnd = true
		}
	}
	if !sawDrainStart || !sawDrainEnd {
		t.Errorf("drain start/end log lines: start=%v end=%v", sawDrainStart, sawDrainEnd)
	}
}

// TestRun_ContextCancel_AlsoDrains proves the ctx.Done() branch of Run's
// select (as distinct from the Signals branch the full-lifecycle test
// above already drives) also triggers a clean drain.
func TestRun_ContextCancel_AlsoDrains(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "d.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, RunOptions{
			Settings: Settings{SocketPath: socketPath},
			PIDPath:  pidPath,
			Clock:    runtime.NewSystemClock(),
			Signals:  make(chan os.Signal), // never fires; ctx cancellation must win
			Ready:    ready,
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
	if _, ok, _ := readPIDFile(pidPath); ok {
		t.Error("pidfile still present after ctx-cancel shutdown")
	}
}

// TestRun_NilManifest_ConstructsOneAndRegisters proves the `if manifest ==
// nil` branch: RunOptions.Manifest left nil must not panic, and Run must
// still complete a full start/signal/drain cycle using the manifest it
// constructs internally.
func TestRun_NilManifest_ConstructsOneAndRegisters(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "d.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")

	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- Run(context.Background(), RunOptions{
			Settings: Settings{SocketPath: socketPath},
			PIDPath:  pidPath,
			Clock:    runtime.NewSystemClock(),
			Signals:  signals,
			Ready:    ready,
			// Manifest intentionally left nil.
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}
	signals <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after a termination signal")
	}
}

// acceptLoop's no-connection-ever-accepted return path (ln.Close() makes
// ln.Accept() error out and the loop return via close(done)) is covered
// transitively by every Run test above. A dedicated direct-call test
// would need a net.Listener value, which needs a "net" import — forbidden
// in this default-unit-lane file (Art.7.2); acceptLoop's connection-
// accepted branch (the atomic increment/decrement around a live conn) is
// therefore left to the `-tags=integration` sibling, which dials a real
// connection through the socket this file only proves is listening.

// assertRunningState proves the daemon is live without dialing it: the
// pidfile exists and the socket file is present at 0600. Extracted to keep
// the lifecycle test under the 50-line cap (Art.10.3).
func assertRunningState(t *testing.T, pidPath, socketPath string) {
	t.Helper()
	// Liveness, proven without dialing: pidfile written, socket file
	// present at the required 0600 permissions.
	if _, ok, _ := readPIDFile(pidPath); !ok {
		t.Error("pidfile not present once Ready fired")
	}
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("socket file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %v, want 0600", perm)
	}

}
