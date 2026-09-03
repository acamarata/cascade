//go:build !windows

package daemon

// Purpose: Restart's two error branches daemon_unix_restart_test.go's
//   success-only ordering test does not reach: Stop failing outright (a
//   typed error propagated straight back, Start never invoked at all) and
//   Start failing after a successful Stop (the combined RestartResult
//   still carries the completed StopResult even though the overall call
//   errors). Split into its own file rather than appended to
//   daemon_unix_restart_test.go purely for Art.10.3's 300-line file cap.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRestart_StopFails_StartNeverInvoked(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("signal boom")
	spawned := 0

	_, err := Restart(context.Background(), RestartOptions{
		Stop: StopOptions{
			PIDPath:    pidPath,
			Prober:     fakeProber{alive: map[int]bool{555: true}},
			Signal:     func(int, syscall.Signal) error { return boom },
			SocketGone: func() bool { return false },
			Sleep:      noopSleep,
		},
		Start: StartOptions{
			PIDPath: pidPath,
			Prober:  fakeProber{},
			Spawn:   func(context.Context) (int, error) { spawned++; return 1, nil },
			Sleep:   noopSleep,
		},
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable (Stop's own signal failure)", err)
	}
	if spawned != 0 {
		t.Errorf("Start.Spawn called %d times, want 0 (Stop failed, Start must never run)", spawned)
	}
}

func TestRestart_StopSucceeds_StartFails_StopResultStillReported(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	spawnBoom := errors.New("spawn boom")

	res, err := Restart(context.Background(), RestartOptions{
		Stop: StopOptions{
			PIDPath:    pidPath,
			Prober:     fakeProber{alive: map[int]bool{555: true}},
			Signal:     func(int, syscall.Signal) error { return nil },
			SocketGone: func() bool { return true },
			Sleep:      noopSleep,
		},
		Start: StartOptions{
			PIDPath: pidPath,
			Prober:  fakeProber{},
			Spawn:   func(context.Context) (int, error) { return 0, spawnBoom },
			Sleep:   noopSleep,
		},
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable (Start's own spawn failure)", err)
	}
	if !res.StopResult.WasRunning {
		t.Errorf("res.StopResult = %+v, want WasRunning=true even though Start failed afterward", res.StopResult)
	}
}
