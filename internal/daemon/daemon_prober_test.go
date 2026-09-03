//go:build !windows

package daemon

// Purpose: unixProber (NewProber/IsAlive/StartTime), the one production
//   ProcessProber every other test file fakes away. These tests exercise
//   the REAL implementation against real OS processes — the current test
//   binary's own PID (os.Getpid(), guaranteed alive for the test's
//   duration) for the liveness/start-time-known path, and a PID no
//   process can plausibly hold (a fixed large number, re-verified not to
//   collide by checking os.FindProcess+Signal(0) first) for the
//   gone/unknown path. No "net" import (Art.7.2's no-network-unit-lane
//   gate does not apply here — this file needs no network at all) and no
//   real signal delivery beyond the standard signal-0 liveness probe every
//   other unix tool (kill -0) already relies on.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"os"
	"syscall"
	"testing"
)

func TestNewProber_ReturnsAUsableProber(t *testing.T) {
	p := NewProber()
	if p == nil {
		t.Fatal("NewProber() = nil")
	}
	if !p.IsAlive(os.Getpid()) {
		t.Error("NewProber().IsAlive(self) = false, want true")
	}
}

func TestUnixProber_IsAlive_CurrentProcessIsAlive(t *testing.T) {
	p := unixProber{}
	if !p.IsAlive(os.Getpid()) {
		t.Errorf("IsAlive(%d) = false, want true (this is the running test process)", os.Getpid())
	}
}

func TestUnixProber_IsAlive_DeadPIDIsNotAlive(t *testing.T) {
	pid := deadPID(t)
	p := unixProber{}
	if p.IsAlive(pid) {
		t.Errorf("IsAlive(%d) = true, want false (no process should hold this pid)", pid)
	}
}

func TestUnixProber_StartTime_CurrentProcessIsKnown(t *testing.T) {
	p := unixProber{}
	got, ok := p.StartTime(os.Getpid())
	if !ok {
		t.Fatal("StartTime(self) ok=false, want true — `ps -o lstart=` must resolve the running test process")
	}
	if got.IsZero() {
		t.Error("StartTime(self) returned the zero time despite ok=true")
	}
}

func TestUnixProber_StartTime_DeadPIDIsUnknown(t *testing.T) {
	pid := deadPID(t)
	p := unixProber{}
	got, ok := p.StartTime(pid)
	if ok {
		t.Errorf("StartTime(%d) ok=true (got %v), want false — no process should hold this pid", pid, got)
	}
}

// deadPID returns a PID this test has verified, via the same signal-0
// probe unixProber.IsAlive itself uses, names no live process — avoiding
// a hardcoded magic number that could (rarely, but really) collide with
// an actual running process on a busy CI box.
func deadPID(t *testing.T) int {
	t.Helper()
	for _, candidate := range []int{1<<31 - 1, 1<<31 - 2, 1<<31 - 3, 999999} {
		proc, err := os.FindProcess(candidate)
		if err != nil {
			return candidate
		}
		if proc.Signal(syscall.Signal(0)) != nil {
			return candidate
		}
	}
	t.Skip("could not find an unused PID candidate on this system")
	return 0
}
