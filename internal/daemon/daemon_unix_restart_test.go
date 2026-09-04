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
	"time"
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

// tracingProber is an exitingProber that records the moment the process it
// models actually exits, so the ordering test can assert that Start's
// Spawn comes strictly after that moment and not merely after the socket
// file disappeared.
type tracingProber struct {
	inner exitingProber
	trace *traceLog
	noted *bool
}

func (p tracingProber) IsAlive(pid int) bool {
	alive := p.inner.IsAlive(pid)
	if !alive && !*p.noted {
		*p.noted = true
		p.trace.add("stop-confirmed-process-exited")
	}
	return alive
}

func (p tracingProber) StartTime(pid int) (time.Time, bool) { return p.inner.StartTime(pid) }

// restartOrderingOptions builds the RestartOptions the ordering test below
// drives, recording each fake's call into trace. The socket is reported
// gone from the first poll onward while the process stays alive for
// several more probes: that is the production ordering (Run unlinks the
// socket from a defer that runs while the composition root still holds
// the store open), and it is exactly the interleaving that used to let
// Start spawn a replacement into a store the old process still owned.
func restartOrderingOptions(pidPath string, trace *traceLog) RestartOptions {
	probes := 0
	noted := false
	return RestartOptions{
		Stop: StopOptions{
			PIDPath: pidPath,
			Prober: tracingProber{
				inner: exitingProber{pid: 555, aliveFor: 4, probes: &probes},
				trace: trace,
				noted: &noted,
			},
			Signal: func(int, syscall.Signal) error {
				trace.add("stop-signal")
				return nil
			},
			SocketGone: func() bool { return true },
			Sleep:      noopSleep,
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
	opts := restartOrderingOptions(pidPath, trace)
	// The premise, asserted rather than assumed: the socket is already
	// reported gone before Stop ever polls, so nothing in the trace below
	// can have been released by the socket predicate.
	if !opts.Stop.SocketGone() {
		t.Fatal("test premise broken: SocketGone must report true from the outset")
	}
	res, err := Restart(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.StopResult.WasRunning || res.StartResult.PID != 777 {
		t.Errorf("res = %+v", res)
	}
	got := trace.snapshot()
	exited := indexOfTrace(got, "stop-confirmed-process-exited")
	spawned := indexOfTrace(got, "start-spawn")
	if exited < 0 || spawned < 0 {
		t.Fatalf("trace = %v, want both a process-exit and a spawn entry", got)
	}
	if got[0] != "stop-signal" {
		t.Fatalf("trace = %v, want it to open with stop-signal", got)
	}
	if spawned < exited {
		t.Fatalf("trace = %v: start-spawn must come strictly after the old process exited", got)
	}
}

// indexOfTrace returns the first index of want in trace, or -1.
func indexOfTrace(trace []string, want string) int {
	for i, s := range trace {
		if s == want {
			return i
		}
	}
	return -1
}
