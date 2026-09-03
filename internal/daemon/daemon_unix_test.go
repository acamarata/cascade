//go:build !windows

package daemon

// Purpose: Start's idempotency and readiness-poll behaviour. Every OS-
//   facing seam (ProcessProber, Spawn, ReadyProbe, Sleep) is faked — no
//   test spawns a real process, and no test sleeps for real (R-14.136):
//   the Sleep fake is a counted no-op. Stop/Restart/Status tests live in
//   their own sibling files (daemon_unix_stop_test.go,
//   daemon_unix_restart_test.go, daemon_unix_status_test.go), split under
//   R-14.117/R-14.133 purely to stay under Art.10.3's 300-line file cap —
//   same package, no behaviour change.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// noopSleep never actually sleeps — the fake every test in this package
// injects so Start/Stop's real (production) backoff never costs test
// wall-clock time.
func noopSleep(time.Duration) {}

func TestStart_AlreadyRunning_IsIdempotentAndDoesNotSpawn(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	started := time.Now()
	if err := writePIDFile(pidPath, pidRecord{PID: 999, StartedAt: started}); err != nil {
		t.Fatal(err)
	}

	spawned := 0
	res, err := Start(context.Background(), StartOptions{
		PIDPath: pidPath,
		Prober:  fakeProber{alive: map[int]bool{999: true}, startTime: map[int]time.Time{999: started}},
		Spawn:   func(context.Context) (int, error) { spawned++; return 1, nil },
		Sleep:   noopSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyRunning || res.PID != 999 {
		t.Errorf("res = %+v, want AlreadyRunning pid=999", res)
	}
	if spawned != 0 {
		t.Errorf("Spawn called %d times, want 0 (idempotent start must not spawn a second daemon)", spawned)
	}
}

func TestStart_StalePIDFile_RemovedThenSpawns(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 111, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	ready := false
	res, err := Start(context.Background(), StartOptions{
		PIDPath:    pidPath,
		Prober:     fakeProber{alive: map[int]bool{111: false}}, // process gone
		Spawn:      func(context.Context) (int, error) { return 222, nil },
		ReadyProbe: func() bool { ready = true; return true },
		Sleep:      noopSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyRunning || res.PID != 222 {
		t.Errorf("res = %+v, want a fresh spawn pid=222", res)
	}
	if !ready {
		t.Error("ReadyProbe was never consulted")
	}
}

func TestStart_ReadinessPollsThenSucceeds(t *testing.T) {
	attempts := 0
	res, err := Start(context.Background(), StartOptions{
		PIDPath: filepath.Join(t.TempDir(), "daemon.pid"),
		Prober:  fakeProber{},
		Spawn:   func(context.Context) (int, error) { return 42, nil },
		ReadyProbe: func() bool {
			attempts++
			return attempts >= 3
		},
		Sleep: noopSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PID != 42 {
		t.Errorf("PID = %d, want 42", res.PID)
	}
	if attempts < 3 {
		t.Errorf("attempts = %d, want >= 3", attempts)
	}
}

func TestStart_ReadinessNeverPasses_BoundedTimeoutError(t *testing.T) {
	sleeps := 0
	_, err := Start(context.Background(), StartOptions{
		PIDPath:    filepath.Join(t.TempDir(), "daemon.pid"),
		Prober:     fakeProber{},
		Spawn:      func(context.Context) (int, error) { return 1, nil },
		ReadyProbe: func() bool { return false },
		Sleep:      func(time.Duration) { sleeps++ },
	})
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
	if sleeps != startMaxAttempts {
		t.Errorf("sleeps = %d, want exactly %d (bounded attempt budget)", sleeps, startMaxAttempts)
	}
}
