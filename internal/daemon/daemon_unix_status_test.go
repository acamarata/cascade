//go:build !windows

package daemon

// Purpose: Status's honesty against stale/recycled pidfiles, plus Run's
//   subsystem-failure path (no real socket needed for this one — Run's
//   real-socket accept/drain/signal round-trip lives in
//   daemon_unix_integration_test.go, the one case that needs a real "net"
//   dial, per Art.7.2's no-network-unit-lane gate). Split from
//   daemon_unix_test.go under R-14.117/R-14.133 (Art.10.3's 300-line file
//   cap) — same package, no behaviour change.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestStatus_NoPIDFile(t *testing.T) {
	res, err := Status(context.Background(), StatusOptions{
		PIDPath: filepath.Join(t.TempDir(), "daemon.pid"),
		Prober:  fakeProber{},
		Clock:   runtime.NewSystemClock(),
	})
	if err != nil || res.Running {
		t.Errorf("res=%+v err=%v, want Running=false", res, err)
	}
}

func TestStatus_Running_ReportsUptime(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	start := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	if err := writePIDFile(pidPath, pidRecord{PID: 1, StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(start.Add(90 * time.Second))
	res, err := Status(context.Background(), StatusOptions{
		PIDPath: pidPath,
		Prober:  fakeProber{alive: map[int]bool{1: true}, startTime: map[int]time.Time{1: start}},
		Clock:   clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Running || res.PID != 1 || res.UptimeS != 90 {
		t.Errorf("res = %+v, want Running pid=1 uptime=90s", res)
	}
}

func TestStatus_StalePIDFile_NeverReportsRunning(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 2, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	res, err := Status(context.Background(), StatusOptions{
		PIDPath: pidPath,
		Prober:  fakeProber{alive: map[int]bool{2: false}},
		Clock:   runtime.NewSystemClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Running {
		t.Error("stale pidfile reported as running")
	}
}

// TestStatus_RecycledPID_NeverReportsRunning is this ticket's required
// pid-recycling case: a different, unrelated process now holds the
// recorded PID (alive=true, but start time does not match).
func TestStatus_RecycledPID_NeverReportsRunning(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	recorded := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := writePIDFile(pidPath, pidRecord{PID: 3, StartedAt: recorded}); err != nil {
		t.Fatal(err)
	}
	res, err := Status(context.Background(), StatusOptions{
		PIDPath: pidPath,
		Prober: fakeProber{
			alive:     map[int]bool{3: true},
			startTime: map[int]time.Time{3: recorded.Add(48 * time.Hour)}, // a much later process now owns pid 3
		},
		Clock: runtime.NewSystemClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Running {
		t.Error("recycled pid reported as running — this is the exact bug the brief warns about")
	}
}

func TestRun_SocketCreationFailure_MarksSubsystemFailed(t *testing.T) {
	// A directory component that cannot be created (a file sitting where a
	// directory needs to go) makes listenSocket fail deterministically.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(blocker, "daemon.sock") // blocker is a file, not a dir

	manifest := NewManifest(nil, runtime.NewSystemClock())
	err := Run(context.Background(), RunOptions{
		Settings: Settings{SocketPath: socketPath},
		PIDPath:  filepath.Join(dir, "daemon.pid"),
		Clock:    runtime.NewSystemClock(),
		Manifest: manifest,
	})
	if err == nil {
		t.Fatal("Run succeeded against an unusable socket path, want an error")
	}
	snap := manifest.Snapshot()
	if len(snap) != 1 || snap[0].State != SubsystemError {
		t.Errorf("manifest snapshot = %+v, want the ipc-socket subsystem in SubsystemError", snap)
	}
}
