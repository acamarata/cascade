//go:build !windows

package daemon

// Purpose: Run-level integration of the upgrade engine into
// lifecycle_unix.go's termination path — proves the wiring end to end,
// not just AttemptUpgrade in isolation. Split from upgrade_test.go purely
// for the 300-line file cap. No "net" import: Run takes a socket PATH and
// manages the listener itself (Art.7.2).
// SPORT: internal/daemon (ADD, per T-5 sport_updates).

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestRun_UpgradeOnSignal_Relaunches drives the real Run loop (not just
// AttemptUpgrade) to prove lifecycle_unix.go's wiring: a termination
// signal with a skewed on-disk binary attempts Drain+Relaunch via the
// stubbed execFunc, and Run returns nil (the line execFunc's stub makes
// reachable; a real successful exec never returns at all).
func TestRun_UpgradeOnSignal_Relaunches(t *testing.T) {
	// Stamp a digest so this exercises the real skew comparison. An
	// unstamped build now reports no skew by design, and without this
	// the test would pass only because the sentinel never equals a hash,
	// which is the bug that made dev builds relaunch on every shutdown.
	setBuildHash(t, "1111111111111111111111111111111111111111111111111111111111111111")
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	var execCalled bool
	execFunc = func(string, []string, []string) error { execCalled = true; return nil }

	h := newRunHarness(t)
	m, _ := newTestManager(t, nil, nil)
	binPath := writeTempBinary(t, "skewed-for-run-test")

	opts := RunOptions{
		Settings:   Settings{SocketPath: h.socketPath, ShutdownGrace: time.Second},
		PIDPath:    h.pidPath,
		Logger:     h.log,
		Clock:      runtime.NewFixedClock(time.Now()),
		Signals:    h.signals,
		Ready:      h.ready,
		Upgrade:    m,
		Executable: func() (string, error) { return binPath, nil },
	}
	go func() { h.done <- Run(context.Background(), opts) }()
	<-h.ready
	h.signals <- os.Interrupt

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the upgrade signal")
	}
	if !execCalled {
		t.Fatal("Run: want execFunc invoked for a skewed on-disk binary")
	}
}

// TestRun_UpgradeRelaunchFails_FallsBackCleanly proves the non-bricking
// requirement at the Run level: when Relaunch fails, Run still completes
// its normal drain-and-exit, and the pidfile/socket are cleaned up so a
// subsequent `daemon start` finds a clean, recoverable state.
func TestRun_UpgradeRelaunchFails_FallsBackCleanly(t *testing.T) {
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	execFunc = func(string, []string, []string) error { return errors.New("exec format error") }

	h := newRunHarness(t)
	m, _ := newTestManager(t, nil, nil)
	binPath := writeTempBinary(t, "skewed-and-unexecutable")

	opts := RunOptions{
		Settings:   Settings{SocketPath: h.socketPath, ShutdownGrace: time.Second},
		PIDPath:    h.pidPath,
		Logger:     h.log,
		Clock:      runtime.NewFixedClock(time.Now()),
		Signals:    h.signals,
		Ready:      h.ready,
		Upgrade:    m,
		Executable: func() (string, error) { return binPath, nil },
	}
	go func() { h.done <- Run(context.Background(), opts) }()
	<-h.ready
	h.signals <- os.Interrupt

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not fall back to a clean exit after a failed relaunch")
	}
	if _, err := os.Stat(h.pidPath); !os.IsNotExist(err) {
		t.Fatalf("pidfile still present after a failed relaunch fallback: %v", err)
	}
	if _, err := os.Stat(h.socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket still present after a failed relaunch fallback: %v", err)
	}
}
