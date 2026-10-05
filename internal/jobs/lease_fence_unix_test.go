//go:build !windows

package jobs

// Purpose: tests lease_fence_unix.go's NewProcessLivenessProbe/IsAlive.
//   The SelfIsAlive and ReapedChildIsDead tests use a REAL process
//   (internal/runtime/lockfile_processalive_unix_test.go's precedent).
//   The TestLivenessProbe* tests swap the livenessKill seam for a
//   recording fake, to pin which (pid, sig) each pgid sends and that
//   every unconfirmed outcome reads as alive; they send no real signal.
// Inputs: the current process's own process group (alive case), a
//   freshly-spawned, already-reaped child's process group (dead case),
//   and injected errnos for the seam tests.
// Outputs: n/a (test file).
// Constraints: must carry the SAME build tag as lease_fence_unix.go
//   (!windows) -- an untagged test over a tagged implementation
//   compiles on windows, where NewProcessLivenessProbe/unixLivenessProbe
//   do not exist, and fails there with undefined symbols.
// SPORT: jobs/lease-model (FIX, coverage floor restoration).

import (
	"math"
	"os/exec"
	"syscall"
	"testing"
)

// TestProcessLivenessProbe_SelfIsAlive proves IsAlive reports true for
// this test process's own process group -- the positive case a
// mocked-syscall test could not distinguish from a bug that always
// returns true.
func TestProcessLivenessProbe_SelfIsAlive(t *testing.T) {
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	probe := NewProcessLivenessProbe()
	if !probe.IsAlive(int64(pgid)) {
		t.Fatalf("IsAlive(self pgid %d) = false, want true", pgid)
	}
}

// TestProcessLivenessProbe_ReapedChildIsDead spawns a child in its OWN
// process group (Setpgid), waits for it to exit and be reaped, then
// asserts IsAlive reports false for that now-gone group -- the actual
// negative constructed against a real process, not inferred from the
// positive case.
func TestProcessLivenessProbe_ReapedChildIsDead(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	deadPgid := cmd.Process.Pid // Setpgid: true makes the child its own group leader, pgid == pid

	probe := NewProcessLivenessProbe()
	if probe.IsAlive(int64(deadPgid)) {
		t.Fatalf("IsAlive(reaped child's pgid %d) = true, want false", deadPgid)
	}
}

// killCall is one recorded livenessKill invocation.
type killCall struct {
	pid int
	sig syscall.Signal
}

// recordKill swaps livenessKill for a recording fake that returns ret
// and never sends a real signal; t.Cleanup restores the real seam.
// Callers must not use t.Parallel, since the seam is a package var.
func recordKill(t *testing.T, ret error) *[]killCall {
	t.Helper()
	calls := &[]killCall{}
	prev := livenessKill
	livenessKill = func(pid int, sig syscall.Signal) error {
		*calls = append(*calls, killCall{pid: pid, sig: sig})
		return ret
	}
	t.Cleanup(func() { livenessKill = prev })
	return calls
}

// assertOneCall fails t unless calls holds exactly one (pid, 0) entry.
func assertOneCall(t *testing.T, calls []killCall, pid int) {
	t.Helper()
	if len(calls) != 1 || calls[0] != (killCall{pid: pid, sig: syscall.Signal(0)}) {
		t.Errorf("recorded calls = %v, want exactly [{%d 0}]", calls, pid)
	}
}

// TestLivenessProbePgidOneProbesPidOne proves pgid 1 probes pid 1 itself
// with signal 0 (never kill(-1, 0), which targets every process), and
// that only ESRCH reads as dead: nil and EPERM are alive, and any other
// errno is unknown, so alive.
func TestLivenessProbePgidOneProbesPidOne(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"EPERM", syscall.EPERM, true},
		{"ESRCH", syscall.ESRCH, false},
		{"EINVAL", syscall.EINVAL, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := recordKill(t, tc.err)
			if got := NewProcessLivenessProbe().IsAlive(1); got != tc.want {
				t.Errorf("IsAlive(1) with %v = %v, want %v", tc.err, got, tc.want)
			}
			assertOneCall(t, *calls, 1)
		})
	}
}

// TestLivenessProbeNonPositivePgidNeverDead proves a pgid the probe
// cannot address (<= 0, or beyond the 32-bit pid_t range) is never
// reported dead and never reaches kill. The fake returns ESRCH, so any
// call that slipped through would read as dead.
func TestLivenessProbeNonPositivePgidNeverDead(t *testing.T) {
	calls := recordKill(t, syscall.ESRCH)
	probe := NewProcessLivenessProbe()
	for _, pgid := range []int64{0, -1, -4242, math.MinInt64, math.MaxInt32 + 1, math.MaxUint32, 1 << 32, 1<<32 + 1} {
		if !probe.IsAlive(pgid) {
			t.Errorf("IsAlive(%d) = false, want true (unprobeable pgid is never dead)", pgid)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("recorded calls = %v, want none", *calls)
	}
}

// TestLivenessProbeNeverKillsAllOrOwnGroup proves no pgid, in or out of
// range, makes the probe call kill with pid -1 (every process) or pid 0
// (the caller's own group), and every pid it does send stays within
// [-math.MaxInt32, 1].
func TestLivenessProbeNeverKillsAllOrOwnGroup(t *testing.T) {
	calls := recordKill(t, nil)
	probe := NewProcessLivenessProbe()
	for _, pgid := range []int64{-1, 0, 1, 2, 4242, math.MaxInt32, math.MaxInt32 + 1, math.MaxUint32, 1 << 32, 1<<32 + 1} {
		probe.IsAlive(pgid)
	}
	for _, c := range *calls {
		if c.pid == -1 || c.pid == 0 || c.pid < -math.MaxInt32 || c.pid > 1 {
			t.Errorf("recorded kill pid %d is -1, 0 or outside [-MaxInt32, 1]", c.pid)
		}
	}
	if len(*calls) != 4 {
		t.Errorf("recorded %d calls, want 4 (pgid 1, 2, 4242, MaxInt32): %v", len(*calls), *calls)
	}
}

// TestLivenessProbeGroupPgidSignalsGroup proves pgid above 1 keeps its
// group semantics: one signal-0 call to -pgid, alive only on nil.
func TestLivenessProbeGroupPgidSignalsGroup(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"ESRCH", syscall.ESRCH, false},
		{"EPERM", syscall.EPERM, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := recordKill(t, tc.err)
			if got := NewProcessLivenessProbe().IsAlive(4242); got != tc.want {
				t.Errorf("IsAlive(4242) with %v = %v, want %v", tc.err, got, tc.want)
			}
			assertOneCall(t, *calls, -4242)
		})
	}
}
