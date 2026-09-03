//go:build !windows && integration

package daemon

// Purpose: Run's real-socket accept/drain/signal round-trip — the one
//   daemon_unix_test.go case that needs actual "net" I/O (a real unix
//   socket dial to prove the listener genuinely accepts connections).
//   Art.7.2 forbids a "net"/"net/http" import in the default unit lane
//   (internal/build's no-network-unit-lane gate scans every _test.go file
//   for it); this repo's established remedy — see internal/runtime's
//   recovery_test.go/recovery_integration_test.go split — is to carve the
//   one test that genuinely needs it into its own `//go:build integration`
//   sibling file, same package, run only with `-tags=integration`. Every
//   OTHER Run()-touching behaviour (subsystem-failure path, drain log
//   lines assertion helpers) that does NOT need "net" stays in
//   daemon_unix_test.go's default lane. Split under R-14.117/R-14.133 (a
//   file the language + Art.7.2 both force into existence).
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestRun_StartAcceptAndSIGTERMDrain_CleanShutdown(t *testing.T) {
	// A unix socket path must fit in sockaddr_un.sun_path (~104 bytes on
	// Darwin) — t.TempDir() embeds this test's own (long) name in its
	// path, which alone can overflow that limit, so the socket lives
	// under a short dedicated temp dir instead (shortTempDir below); only
	// the pidfile uses the normal, longer t.TempDir().
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "d.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")

	log, records := newRecordingLogger()
	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)

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

	// Select on both readiness AND early exit: a bare `<-ready` would hang
	// forever if Run returns an error before ever closing Ready (exactly
	// the failure mode this select makes impossible to miss).
	select {
	case <-ready: // no sleep: synchronized on the real readiness signal
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}

	assertSocketLiveAndReady(t, socketPath, pidPath)

	signals <- os.Interrupt // SIGTERM/SIGINT-equivalent test signal

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after a termination signal — daemon never drained")
	}

	assertCleanShutdown(t, socketPath, pidPath, records)
}

// assertSocketLiveAndReady asserts the socket dials, the pidfile exists,
// and the socket carries 0600 permissions (06-FORGE-SPEC §2) — everything
// that must be true the instant Run's Ready channel fires.
func assertSocketLiveAndReady(t *testing.T, socketPath, pidPath string) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial live socket: %v", err)
	}
	_ = conn.Close()

	if _, ok, _ := readPIDFile(pidPath); !ok {
		t.Error("pidfile not present once Ready fired")
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %v, want 0600", perm)
	}
}

// assertCleanShutdown asserts both filesystem artifacts are gone and both
// drain log lines were emitted, once Run has returned after a termination
// signal.
func assertCleanShutdown(t *testing.T, socketPath, pidPath string, records *[]slog.Record) {
	t.Helper()
	if _, ok, _ := readPIDFile(pidPath); ok {
		t.Error("pidfile still present after clean shutdown")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file still present after clean shutdown")
	}

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
