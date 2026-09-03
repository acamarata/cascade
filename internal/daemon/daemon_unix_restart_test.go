//go:build !windows

package daemon

// Purpose: Restart's no-gap-no-overlap ordering guarantee. Split from
//   daemon_unix_test.go under R-14.117/R-14.133 (Art.10.3's 300-line file
//   cap) — same package, no behaviour change.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// traceLog is a concurrency-safe append-only string log the restart-
// ordering test below uses to record call order across Stop's and Start's
// injected fakes, which may run from different goroutines.
type traceLog struct {
	mu  sync.Mutex
	log []string
}

func (t *traceLog) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.log = append(t.log, s)
}

func (t *traceLog) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.log...)
}

// restartOrderingOptions builds the RestartOptions the ordering test below
// drives, recording each fake's call into trace.
func restartOrderingOptions(pidPath string, trace *traceLog) RestartOptions {
	polls := 0
	return RestartOptions{
		Stop: StopOptions{
			PIDPath: pidPath,
			Prober:  fakeProber{alive: map[int]bool{555: true}},
			Signal: func(int, syscall.Signal) error {
				trace.add("stop-signal")
				return nil
			},
			SocketGone: func() bool {
				polls++
				gone := polls > 1
				if gone {
					trace.add("stop-confirmed-gone")
				}
				return gone
			},
			Sleep: noopSleep,
		},
		Start: StartOptions{
			PIDPath: pidPath, // same path: stale-check re-runs, finds none
			Prober:  fakeProber{},
			Spawn: func(context.Context) (int, error) {
				trace.add("start-spawn")
				return 777, nil
			},
			ReadyProbe: func() bool { return true },
			Sleep:      noopSleep,
		},
	}
}

// TestRestart_StopFullyCompletesBeforeStartSpawns proves the ordering
// guarantee: Start's Spawn is never invoked until Stop has already
// observed the socket gone, so a Restart can never produce two live
// daemons at once.
func TestRestart_StopFullyCompletesBeforeStartSpawns(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}

	trace := &traceLog{}
	res, err := Restart(context.Background(), restartOrderingOptions(pidPath, trace))
	if err != nil {
		t.Fatal(err)
	}
	if !res.StopResult.WasRunning || res.StartResult.PID != 777 {
		t.Errorf("res = %+v", res)
	}
	want := []string{"stop-signal", "stop-confirmed-gone", "start-spawn"}
	got := trace.snapshot()
	if len(got) != len(want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("trace = %v, want %v (start-spawn must come strictly after stop-confirmed-gone)", got, want)
		}
	}
}
